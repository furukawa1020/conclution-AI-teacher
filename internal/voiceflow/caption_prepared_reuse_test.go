package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

func waitCaptionPreparationReady(t *testing.T, handoff *captionHandoff) {
	t.Helper()
	handoff.mu.Lock()
	preparation := handoff.synthesisPreparation
	handoff.mu.Unlock()
	if preparation == nil {
		t.Fatal("caption handoff did not open content-free preparation")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		preparation.mu.Lock()
		ready := preparation.prepared != nil && !preparation.closed
		preparation.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("caption handoff preparation did not become ready")
}

func TestCaptionHandoffCommittedReplyReusesOnlyUnusedPreparation(t *testing.T) {
	for _, test := range []struct {
		name                  string
		observeCandidate      bool
		blockSpeculativeModel bool
	}{
		{name: "final-only caption"},
		{name: "revision before speculative TTS", observeCandidate: true, blockSpeculativeModel: true},
		{name: "revision after preparation consumed", observeCandidate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared := &fakePreparedSynthesis{chunks: [][]byte{{64, 0}}}
			speech := &preparingStreamingSpeech{
				fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{99, 0}}},
				prepared:            prepared,
			}
			committedDecision := captionHandoffRespondentDecision()
			committedDecision.SpokenReply = "committed reply"
			committedDecision.StateToken = "committed-state"
			speculativeDecision := captionHandoffRespondentDecision()
			speculativeDecision.SpokenReply = "superseded reply"
			speculativeDecision.StateToken = "speculative-state"
			agent := &speculativeTestAgent{
				normalResult:      committedDecision,
				speculativeResult: speculativeDecision,
				blockSpeculative:  test.blockSpeculativeModel,
				started:           make(chan struct{}),
				cancelled:         make(chan struct{}),
			}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			processingCommitted := make(chan struct{})
			var checkpointAccepted atomic.Bool
			var checkpointCalls atomic.Int32
			var delivered []byte
			opened, err := pipeline.OpenCaptionHandoff(
				context.Background(), "uid-caption-prepared-reuse",
				httpapi.VoiceTurnInput{
					MIMEType:            speechio.StreamingAudioContentType,
					NativeAudio:         true,
					ProcessingCommitted: processingCommitted,
				},
				func(chunk []byte) error {
					if !checkpointAccepted.Load() {
						return errors.New("prepared PCM crossed before committed checkpoint")
					}
					delivered = append(delivered, chunk...)
					return nil
				},
				func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
					if checkpoint.SessionState != "committed-state" {
						return errors.New("superseded candidate checkpoint escaped")
					}
					checkpointCalls.Add(1)
					checkpointAccepted.Store(true)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			handoff := opened.(*captionHandoff)
			defer handoff.Cancel()
			waitCaptionPreparationReady(t, handoff)

			const candidate = "My manager asked why."
			const finalCaption = "My manager asked when."
			if test.observeCandidate {
				if err := handoff.Observe([]byte(candidate), false, time.Now()); err != nil {
					t.Fatal(err)
				}
				select {
				case <-agent.started:
				case <-time.After(time.Second):
					t.Fatal("speculative model did not start")
				}
				if !test.blockSpeculativeModel {
					handoff.mu.Lock()
					private := handoff.speculation
					handoff.mu.Unlock()
					select {
					case outcome := <-private.outcome:
						if outcome.err != nil || outcome.synthesis == nil {
							t.Fatalf("speculative outcome = %+v", outcome)
						}
						select {
						case <-outcome.synthesis.done:
						case <-time.After(time.Second):
							t.Fatal("prepared speculative synthesis did not finish")
						}
					case <-time.After(time.Second):
						t.Fatal("speculative model did not finish")
					}
				}
			}
			if err := handoff.Observe([]byte(finalCaption), true, time.Now()); err != nil {
				t.Fatal(err)
			}
			if test.blockSpeculativeModel {
				select {
				case <-agent.cancelled:
				case <-time.After(time.Second):
					t.Fatal("caption revision did not cancel the speculative model")
				}
			}
			if len(delivered) != 0 || checkpointCalls.Load() != 0 {
				t.Fatalf("output escaped before transport commit: audio=%v checkpoints=%d", delivered, checkpointCalls.Load())
			}
			close(processingCommitted)
			result, err := handoff.Commit()
			if err != nil {
				t.Fatal(err)
			}

			wantPreparedText := committedDecision.SpokenReply
			wantFallbackCalls := 0
			wantAudio := []byte{64, 0}
			if test.observeCandidate && !test.blockSpeculativeModel {
				wantPreparedText = speculativeDecision.SpokenReply
				wantFallbackCalls = 1
				wantAudio = []byte{99, 0}
			}
			prepared.mu.Lock()
			preparedCalls, preparedText, preparedClosed := prepared.calls, prepared.text, prepared.closed
			prepared.mu.Unlock()
			if preparedCalls != 1 || preparedText != wantPreparedText || !preparedClosed ||
				speech.streamCalls != wantFallbackCalls || !bytes.Equal(delivered, wantAudio) {
				t.Fatalf("prepared calls=%d text=%q closed=%t fallback=%d audio=%v", preparedCalls, preparedText, preparedClosed, speech.streamCalls, delivered)
			}
			if result.Caption != committedDecision.SpokenReply || checkpointCalls.Load() != 1 ||
				result.LiveTimings.SpecHit != 0 || result.LiveTimings.SpecMiss != 1 {
				t.Fatalf("result=%+v checkpoints=%d", result, checkpointCalls.Load())
			}
			turns := agent.recordedTurns()
			wantTurns := 1
			if test.observeCandidate {
				wantTurns = 2
				if len(turns) == 0 || !turns[0].Speculative || turns[0].Utterance != candidate {
					t.Fatalf("unexpected speculative turn: %+v", turns)
				}
			}
			if len(turns) != wantTurns || turns[len(turns)-1].Speculative ||
				turns[len(turns)-1].Utterance != finalCaption {
				t.Fatalf("unexpected committed turns: %+v", turns)
			}
		})
	}
}
