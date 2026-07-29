package voiceflow

import (
	"context"
	"errors"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
)

type fakeSpeech struct {
	transcript      string
	transcribeErr   error
	synthesizeCalls int
}

func (s *fakeSpeech) Transcribe(_ context.Context, _ []byte) (string, float32, error) {
	return s.transcript, 0.95, s.transcribeErr
}

func (s *fakeSpeech) Synthesize(_ context.Context, _ string) ([]byte, string, error) {
	s.synthesizeCalls++
	return []byte("speech"), "audio/mpeg", nil
}

type fakeAgent struct {
	turn   conversation.VoiceTurn
	result conversation.VoiceTurnResult
	err    error
}

func (a *fakeAgent) Process(
	_ context.Context,
	_ string,
	turn conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	a.turn = turn
	return a.result, a.err
}

func TestPipelinePreservesDeliberateSilence(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "いや、なんか締切が不安で"}
	agent := &fakeAgent{
		result: conversation.VoiceTurnResult{
			Domain:     "daily",
			Route:      "fast",
			StateToken: "encrypted-state",
			Intervention: conversation.ArbiterDecision{
				Act: "silent",
			},
		},
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio:      []byte("audio"),
		MIMEType:   "audio/webm",
		Ambient:    true,
		STTLocale:  "ja-JP",
		StateToken: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Audio) != 0 || result.AudioMIMEType != "" {
		t.Fatalf("silent result contains audio: %+v", result)
	}
	if speech.synthesizeCalls != 0 {
		t.Fatalf("synthesis calls = %d; want 0", speech.synthesizeCalls)
	}
	if !agent.turn.Ambient || agent.turn.Utterance == "" {
		t.Fatalf("agent turn = %+v", agent.turn)
	}
}

func TestPipelineSynthesizesOnlySelectedIntervention(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "この論文の結果は因果関係を示したと思う"}
	agent := &fakeAgent{
		result: conversation.VoiceTurnResult{
			Domain:      "research",
			Route:       "precision",
			StateToken:  "encrypted-state",
			SpokenReply: "その結果だけでは相関かも。手法を一度見よう。",
			Intervention: conversation.ArbiterDecision{
				Act: "paper_check",
			},
		},
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio:    []byte("audio"),
		MIMEType: "audio/webm",
		Ambient:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Audio) != "speech" || result.AudioMIMEType != "audio/mpeg" {
		t.Fatalf("result = %+v", result)
	}
	if !result.NeedsPaper {
		t.Fatal("research intervention should request the explicitly supplied paper")
	}
	if speech.synthesizeCalls != 1 {
		t.Fatalf("synthesis calls = %d; want 1", speech.synthesizeCalls)
	}
}

func TestPipelineRejectsInvalidEncryptedState(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "続きです"}
	agent := &fakeAgent{err: conversation.ErrInvalidStateToken}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio: []byte("audio"),
	})
	if !errors.Is(err, httpapi.ErrVoiceStateInvalid) {
		t.Fatalf("error = %v; want invalid state", err)
	}
}
