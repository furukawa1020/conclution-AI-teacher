package voiceflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type questionBoundTestAgent struct {
	*fakeAgent
	capability conversation.QuestionBoundPhraseCapability
	err        error
	calls      int
}

func (a *questionBoundTestAgent) IssueQuestionBoundPhraseCapability(
	_ string,
	_ string,
	_ string,
) (conversation.QuestionBoundPhraseCapability, error) {
	a.calls++
	return a.capability, a.err
}

type questionBoundTestSpeech struct {
	fakeSpeech
	ordinaryCalls int
	adaptedCalls  int
	digest        [sha256.Size]byte
	generation    string
}

func (s *questionBoundTestSpeech) Transcribe(
	_ context.Context,
	_ []byte,
) (string, float32, error) {
	s.ordinaryCalls++
	return s.fakeSpeech.Transcribe(context.Background(), nil)
}

func (s *questionBoundTestSpeech) TranscribeQuestionBound(
	_ context.Context,
	_ []byte,
	_ *speechio.QuestionBoundPhraseSet,
	_ time.Time,
	digest [sha256.Size]byte,
	generation string,
) (string, float32, error) {
	s.adaptedCalls++
	s.digest = digest
	s.generation = generation
	return "音楽", .91, nil
}

func TestBufferedRecognitionUsesOnlyAuthenticatedQuestionCapability(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("displayed-question"))
	agent := &questionBoundTestAgent{
		fakeAgent: &fakeAgent{},
		capability: conversation.QuestionBoundPhraseCapability{
			QuestionDigest: digest,
			TurnGeneration: "0123456789abcdef01234567",
			ExpiresAt:      now.Add(time.Minute),
			QuestionTerms:  []string{"音楽", "読書"},
			UserTerms:      []string{"静か"},
		},
	}
	speech := &questionBoundTestSpeech{}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	transcript, confidence, err := pipeline.transcribeBuffered(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{
			Audio:      []byte{1, 2, 3},
			StateToken: "encrypted-state",
			RequestID:  "0123456789abcdef01234567",
		},
	)
	if err != nil || transcript != "音楽" || confidence != .91 {
		t.Fatalf("result = %q, %v, %v", transcript, confidence, err)
	}
	if agent.calls != 1 || speech.adaptedCalls != 1 || speech.ordinaryCalls != 0 ||
		speech.digest != digest || speech.generation != agent.capability.TurnGeneration {
		t.Fatalf("routing mismatch: agent=%d adapted=%d ordinary=%d", agent.calls, speech.adaptedCalls, speech.ordinaryCalls)
	}
}

func TestBufferedRecognitionFallsBackForUnavailableCapability(t *testing.T) {
	agent := &questionBoundTestAgent{
		fakeAgent: &fakeAgent{},
		err:       conversation.ErrInvalidStateToken,
	}
	speech := &questionBoundTestSpeech{fakeSpeech: fakeSpeech{
		transcript: "通常認識",
		confidence: .88,
	}}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	transcript, confidence, err := pipeline.transcribeBuffered(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{
			Audio:      []byte{1},
			StateToken: "old-state",
			RequestID:  "0123456789abcdef01234567",
		},
	)
	if err != nil || transcript != "通常認識" || confidence != .88 ||
		agent.calls != 1 || speech.adaptedCalls != 0 || speech.ordinaryCalls != 1 {
		t.Fatalf("fallback mismatch: %q %v %v agent=%d adapted=%d ordinary=%d", transcript, confidence, err, agent.calls, speech.adaptedCalls, speech.ordinaryCalls)
	}

	agent.err = nil
	agent.capability = conversation.QuestionBoundPhraseCapability{
		QuestionDigest: sha256.Sum256([]byte("question")),
		TurnGeneration: "0123456789abcdef01234567",
		ExpiresAt:      time.Now().Add(10 * time.Minute),
		QuestionTerms:  []string{"期限外"},
	}
	_, _, err = pipeline.transcribeBuffered(
		context.Background(), "uid", httpapi.VoiceTurnInput{
			Audio: []byte{1}, StateToken: "state", RequestID: "0123456789abcdef01234567",
		},
	)
	if err != nil && !errors.Is(err, speechio.ErrNoSpeech) {
		t.Fatalf("invalid local set did not use ordinary fallback: %v", err)
	}
	if speech.adaptedCalls != 0 || speech.ordinaryCalls != 2 {
		t.Fatalf("invalid set routing: adapted=%d ordinary=%d", speech.adaptedCalls, speech.ordinaryCalls)
	}
}
