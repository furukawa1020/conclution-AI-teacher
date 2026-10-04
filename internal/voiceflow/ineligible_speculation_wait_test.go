package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

// heldOutcomeAgent deliberately ignores cancellation until the test releases
// it. A rejected model request must not delay the committed response.
type heldOutcomeAgent struct {
	*speculativeTestAgent
	allowReturn <-chan struct{}
	contexts    chan context.Context
	returned    chan struct{}
}

func (agent *heldOutcomeAgent) Process(
	ctx context.Context, uid string, turn conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	decision, err := agent.speculativeTestAgent.Process(ctx, uid, turn)
	if turn.Speculative {
		agent.contexts <- ctx
		<-agent.allowReturn
		close(agent.returned)
	}
	return decision, err
}

type heldSealedOutcomeAgent struct{ *heldOutcomeAgent }

func (agent *heldSealedOutcomeAgent) ProcessWithSealedCandidate(
	ctx context.Context, uid string, turn conversation.VoiceTurn,
	onCandidate func(conversation.SealedSpeechCandidate),
) (conversation.VoiceTurnResult, error) {
	decision, err := agent.Process(ctx, uid, turn)
	if err == nil {
		onCandidate(conversation.SealedSpeechCandidate{SpokenReply: decision.SpokenReply})
	}
	return decision, err
}

func TestLiveSpeculationWaitRequiresProviderEndpoint(t *testing.T) {
	for _, endpoint := range []bool{false, true} {
		name := "missing endpoint skips held model"
		if endpoint {
			name = "verified endpoint adopts held model"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			allowReturn := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(allowReturn) }) }
			agent := &heldOutcomeAgent{
				speculativeTestAgent: &speculativeTestAgent{
					speculativeResult: liveTestDecision("先読みの回答", "spec-state"),
					normalResult:      liveTestDecision("確定後の回答", "normal-state"),
					started:           make(chan struct{}),
				},
				allowReturn: allowReturn,
				contexts:    make(chan context.Context, 1),
				returned:    make(chan struct{}),
			}
			const utterance = "この質問を詳しく説明して"
			session := newFakeLiveTranscriptionSession(
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionInterim, Text: utterance, Stability: .95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionInterim, Text: utterance, Stability: .95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionFinal, Text: utterance, Confidence: .98,
				},
			)
			session.eventGates = map[int]<-chan struct{}{2: agent.started}
			if endpoint {
				appendProviderSpeechEnd(session)
			}
			speech := &scriptedLiveSpeech{
				session: session,
				scripts: []scriptedSynthesis{{chunks: [][]byte{{33, 0}}}},
			}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Unix(250, 0)
			pipeline.now = sequenceClock(started, started.Add(minSpeculativeStableDuration))
			audio := make(chan []byte, 1)
			audio <- []byte{1, 0}
			close(audio)
			var outputMu sync.Mutex
			var output []byte
			type outcome struct {
				result httpapi.VoiceTurnResult
				err    error
			}
			done := make(chan outcome, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("live pipeline did not stop")
				}
			})
			go func() {
				defer close(finished)
				result, err := pipeline.ProcessLive(ctx, "held-model-test",
					httpapi.VoiceTurnInput{RequestID: "held-model", StateToken: "existing-state"}, audio,
					func(chunk []byte) error {
						outputMu.Lock()
						defer outputMu.Unlock()
						output = append(output, chunk...)
						return nil
					})
				done <- outcome{result, err}
			}()
			var speculativeCtx context.Context
			select {
			case speculativeCtx = <-agent.contexts:
			case <-time.After(time.Second):
				t.Fatal("speculative model did not start")
			}
			if endpoint {
				release()
			}
			var completed outcome
			select {
			case completed = <-done:
			case <-time.After(time.Second):
				t.Fatal("committed response waited for an ineligible model outcome")
			}
			if completed.err != nil {
				t.Fatal(completed.err)
			}
			wantState, wantCaption := "normal-state", "確定後の回答"
			wantHit, wantMiss, wantCancel := int64(0), int64(1), int64(1)
			if endpoint {
				wantState, wantCaption = "spec-state", "先読みの回答"
				wantHit, wantMiss, wantCancel = 1, 0, 0
			} else {
				select {
				case <-agent.returned:
					t.Fatal("model was released before the committed response completed")
				default:
				}
				if !errors.Is(speculativeCtx.Err(), context.Canceled) {
					t.Fatal("ineligible speculative context was not canceled")
				}
			}
			result := completed.result
			if result.StateToken != wantState || result.Caption != wantCaption ||
				result.LiveTimings.SpecHit != wantHit || result.LiveTimings.SpecMiss != wantMiss ||
				result.LiveTimings.SpecCancel != wantCancel {
				t.Fatalf("result=%+v", result)
			}
			turns := agent.recordedTurns()
			if endpoint {
				if len(turns) != 1 || !turns[0].Speculative {
					t.Fatalf("eligible speculation was rerun: turns=%+v", turns)
				}
			} else if len(turns) != 2 || !turns[0].Speculative || turns[1].Speculative ||
				turns[1].FloorEvidence == conversation.FloorEvidenceHybridCommitted {
				t.Fatalf("unverified endpoint gained authority: turns=%+v", turns)
			}
			outputMu.Lock()
			defer outputMu.Unlock()
			if !bytes.Equal(output, []byte{33, 0}) {
				t.Fatalf("unexpected PCM: %v", output)
			}
			texts := speech.synthesisTexts()
			if len(texts) != 1 || texts[0] != wantCaption {
				t.Fatalf("unexpected synthesized text: %v", texts)
			}
		})
	}
}

func TestCanceledLiveSpeculationRejectsLateModelOutcome(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		name := "final model outcome"
		if sealed {
			name = "sealed candidate and final model outcome"
		}
		t.Run(name, func(t *testing.T) {
			allowReturn := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(allowReturn) }) }
			defer release()
			heldAgent := &heldOutcomeAgent{
				speculativeTestAgent: &speculativeTestAgent{
					speculativeResult: liveTestDecision("公開してはいけない遅着回答", "late-state"),
				},
				allowReturn: allowReturn,
				contexts:    make(chan context.Context, 1),
				returned:    make(chan struct{}),
			}
			var agent conversation.Agent = heldAgent
			if sealed {
				agent = &heldSealedOutcomeAgent{heldAgent}
			}
			speech := &scriptedLiveSpeech{}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			var outputMu sync.Mutex
			delivered := 0
			speculation := pipeline.startLiveSpeculation(context.Background(), "late-model-test",
				httpapi.VoiceTurnInput{StateToken: "existing-state"}, "この質問を詳しく説明して", speech,
				func([]byte) error {
					outputMu.Lock()
					defer outputMu.Unlock()
					delivered++
					return nil
				}, nil)
			if speculation == nil {
				t.Fatal("speculation did not start")
			}
			defer speculation.cancel()
			select {
			case <-heldAgent.contexts:
			case <-time.After(time.Second):
				t.Fatal("speculative model did not start")
			}
			speculation.cancel()
			release()
			select {
			case outcome := <-speculation.outcome:
				if !errors.Is(outcome.err, context.Canceled) || outcome.synthesis != nil || outcome.initiative != nil {
					t.Fatalf("late outcome retained publication capability: %+v", outcome)
				}
			case <-time.After(time.Second):
				t.Fatal("late model outcome did not complete")
			}
			if texts := speech.synthesisTexts(); len(texts) != 0 {
				t.Fatalf("late result started synthesis: %v", texts)
			}
			outputMu.Lock()
			defer outputMu.Unlock()
			if delivered != 0 {
				t.Fatalf("late result published %d PCM chunks", delivered)
			}
		})
	}
}
