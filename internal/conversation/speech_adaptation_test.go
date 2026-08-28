package conversation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQuestionBoundPhraseCapabilityIsAuthenticatedFiniteAndExpiring(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	agent.codec.now = func() time.Time { return now }

	frame := agent.newSpeechAdaptationFrame(
		"静か",
		"最近は音楽と読書のどちらが少し楽ですか？",
		2,
	)
	if frame.QuestionDigest == "" || frame.Turn != 2 {
		t.Fatalf("unexpected finite phrase frame: %#v", frame)
	}
	stateToken, err := agent.sealState("uid-phrase", conversationState{
		Turn:             2,
		Graph:            emptySpeechAdaptationTestGraph(),
		PendingAnswer:    emptyPendingAnswer(),
		SpeechAdaptation: frame,
		LastIntervention: ArbiterDecision{Act: "silent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stateToken, "音楽") || strings.Contains(stateToken, "読書") ||
		strings.Contains(stateToken, "静か") {
		t.Fatal("encrypted state exposed phrase plaintext")
	}

	const generation = "0123456789abcdef01234567"
	capability, err := agent.IssueQuestionBoundPhraseCapability(
		"uid-phrase",
		stateToken,
		generation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if capability.TurnGeneration != generation ||
		capability.QuestionDigest == [32]byte{} ||
		!capability.ExpiresAt.Equal(now.Add(speechAdaptationTTL)) ||
		len(capability.QuestionTerms) == 0 ||
		len(capability.UserTerms) != 1 {
		t.Fatalf("unexpected capability: %#v", capability)
	}
	if _, err := agent.IssueQuestionBoundPhraseCapability(
		"another-uid", stateToken, generation,
	); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("wrong UID error = %v", err)
	}
	if _, err := agent.IssueQuestionBoundPhraseCapability(
		"uid-phrase", stateToken, "not-a-server-request-id",
	); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("invalid generation error = %v", err)
	}

	agent.codec.now = func() time.Time { return now.Add(speechAdaptationTTL) }
	if _, err := agent.IssueQuestionBoundPhraseCapability(
		"uid-phrase", stateToken, generation,
	); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("expired capability error = %v", err)
	}
}

func TestSpeechAdaptationDigestBindsQuestionAndTurn(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	first := agent.speechQuestionDigest("どちらですか？", 1)
	if first == [32]byte{} {
		t.Fatal("digest was empty")
	}
	if first == agent.speechQuestionDigest("どちらですか？", 2) {
		t.Fatal("digest was not bound to turn")
	}
	if first == agent.speechQuestionDigest("いつですか？", 1) {
		t.Fatal("digest was not bound to displayed question")
	}
}

func TestSpeechAdaptationStateIsNotWrittenBeforeStateV2Rollout(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = false
	frame := agent.newSpeechAdaptationFrame(
		"音楽", "音楽と読書のどちらですか？", 1,
	)
	token, err := agent.sealState("uid-rollout", conversationState{
		Turn:             1,
		Graph:            emptySpeechAdaptationTestGraph(),
		PendingAnswer:    emptyPendingAnswer(),
		SpeechAdaptation: frame,
		LastIntervention: ArbiterDecision{Act: "silent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := agent.codec.open("uid-rollout", token)
	if err != nil {
		t.Fatal(err)
	}
	if opened.SpeechAdaptation.QuestionDigest != "" ||
		opened.SpeechAdaptation.IssuedAt != 0 ||
		opened.SpeechAdaptation.ExpiresAt != 0 ||
		opened.SpeechAdaptation.Turn != 0 {
		t.Fatalf("pre-rollout state emitted adaptation: %#v", opened.SpeechAdaptation)
	}
}

func emptySpeechAdaptationTestGraph() ThoughtStateGraph {
	return ThoughtStateGraph{
		Goals:          []string{},
		Claims:         []string{},
		Grounds:        []string{},
		Assumptions:    []string{},
		Constraints:    []string{},
		OpenLoops:      []string{},
		Contradictions: []string{},
		Decisions:      []string{},
	}
}
