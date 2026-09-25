package voiceflow

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type auditedCandidateTestAgent struct {
	candidate string
	final     conversation.VoiceTurnResult
	started   <-chan string

	mu                        sync.Mutex
	observedTTSBeforeFinal    bool
	observedCandidateCallback bool
}

func (agent *auditedCandidateTestAgent) Process(
	_ context.Context,
	_ string,
	_ conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	return agent.final, nil
}

func (agent *auditedCandidateTestAgent) ProcessWithSealedCandidate(
	ctx context.Context,
	_ string,
	_ conversation.VoiceTurn,
	onCandidate func(conversation.SealedSpeechCandidate),
) (conversation.VoiceTurnResult, error) {
	agent.mu.Lock()
	agent.observedCandidateCallback = true
	agent.mu.Unlock()
	onCandidate(conversation.SealedSpeechCandidate{SpokenReply: agent.candidate})
	select {
	case text := <-agent.started:
		agent.mu.Lock()
		agent.observedTTSBeforeFinal = text == agent.candidate
		agent.mu.Unlock()
	case <-ctx.Done():
		return conversation.VoiceTurnResult{}, ctx.Err()
	case <-time.After(time.Second):
		return conversation.VoiceTurnResult{}, context.DeadlineExceeded
	}
	return agent.final, nil
}

type auditedCandidateSpeech struct {
	fakeSpeech
	started chan string

	mu    sync.Mutex
	calls []string
}

func (speech *auditedCandidateSpeech) StreamSynthesize(
	ctx context.Context,
	text string,
	onChunk speechio.StreamChunkHandler,
) (string, error) {
	speech.mu.Lock()
	call := len(speech.calls)
	speech.calls = append(speech.calls, text)
	speech.mu.Unlock()
	select {
	case speech.started <- text:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	chunk := []byte{byte(call + 1), 0}
	if err := onChunk(chunk); err != nil {
		return "", err
	}
	return speechio.StreamingAudioContentType, nil
}

func TestAuditedCandidateStartsTTSBeforeFinalResultAndAdoptsExactMatch(t *testing.T) {
	started := make(chan string, 2)
	speech := &auditedCandidateSpeech{started: started}
	final := liveTestDecision("監査済みの短い返答", "candidate-match")
	agent := &auditedCandidateTestAgent{
		candidate: final.SpokenReply,
		final:     final,
		started:   started,
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	var delivered []byte
	speculation := pipeline.startLiveSpeculation(
		context.Background(),
		"audited-candidate-user",
		httpapi.VoiceTurnInput{RequestID: "audited-candidate-match"},
		"十分に長い安定済みの入力候補",
		speech,
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
			return nil
		},
		nil,
	)
	outcome := <-speculation.outcome
	if outcome.err != nil || outcome.synthesis == nil {
		t.Fatalf("outcome err=%v synthesis=%t", outcome.err, outcome.synthesis != nil)
	}
	result := outcome.synthesis.await(context.Background())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(delivered) != 0 {
		t.Fatalf("candidate audio escaped before commit: %v", delivered)
	}
	if _, err := outcome.synthesis.buffer.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	preparedBeforeFinal := agent.observedTTSBeforeFinal
	callbackObserved := agent.observedCandidateCallback
	agent.mu.Unlock()
	if !preparedBeforeFinal || !callbackObserved || !bytes.Equal(delivered, []byte{1, 0}) {
		t.Fatalf(
			"callback=%t before_final=%t delivered=%v",
			callbackObserved,
			preparedBeforeFinal,
			delivered,
		)
	}
}

func TestAuditedCandidateMismatchDiscardsPCMAndSynthesizesFinalReply(t *testing.T) {
	started := make(chan string, 3)
	speech := &auditedCandidateSpeech{started: started}
	final := liveTestDecision("最終監査で確定した返答", "candidate-mismatch")
	agent := &auditedCandidateTestAgent{
		candidate: "早期候補の返答",
		final:     final,
		started:   started,
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	var delivered []byte
	speculation := pipeline.startLiveSpeculation(
		context.Background(),
		"audited-candidate-user",
		httpapi.VoiceTurnInput{RequestID: "audited-candidate-mismatch"},
		"十分に長い安定済みの入力候補",
		speech,
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
			return nil
		},
		nil,
	)
	outcome := <-speculation.outcome
	if outcome.err != nil || outcome.synthesis == nil {
		t.Fatalf("outcome err=%v synthesis=%t", outcome.err, outcome.synthesis != nil)
	}
	result := outcome.synthesis.await(context.Background())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(delivered) != 0 {
		t.Fatalf("mismatched candidate audio escaped before commit: %v", delivered)
	}
	if _, err := outcome.synthesis.buffer.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	speech.mu.Lock()
	calls := append([]string(nil), speech.calls...)
	speech.mu.Unlock()
	if len(calls) != 2 || calls[0] != agent.candidate || calls[1] != final.SpokenReply ||
		!bytes.Equal(delivered, []byte{2, 0}) {
		t.Fatalf("calls=%v delivered=%v", calls, delivered)
	}
}
