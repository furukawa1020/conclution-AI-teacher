package voiceflow

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

// stalledCanceledSpeech models a provider that acknowledges cancellation only
// after a delayed callback. Its shutdown must not hold the committed turn.
type stalledCanceledSpeech struct {
	*scriptedLiveSpeech
	buffered     chan struct{}
	allowReturn  chan struct{}
	lateCallback chan error
}

func (speech *stalledCanceledSpeech) StreamSynthesize(
	ctx context.Context,
	text string,
	onChunk speechio.StreamChunkHandler,
) (string, error) {
	if text != "discarded reply" {
		return speech.scriptedLiveSpeech.StreamSynthesize(ctx, text, onChunk)
	}
	if err := onChunk([]byte{7, 0}); err != nil {
		return "", err
	}
	close(speech.buffered)
	<-speech.allowReturn
	err := onChunk([]byte{9, 0})
	speech.lateCallback <- err
	return "", ctx.Err()
}

func TestLiveRejectedSpeculationDoesNotWaitForCanceledProvider(t *testing.T) {
	for _, test := range []struct {
		name       string
		final      string
		confidence float32
		wantRoute  string
		wantState  string
	}{
		{"low confidence", "この質問を詳しく説明して", .40, routeClarifyLowConfidence, "existing-state"},
		{"no final speech", "", 0, routeClarifyNoSpeech, "existing-state"},
		{"no provider endpoint", "この質問を詳しく説明して", .98, "fast", "normal-state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			buffered := make(chan struct{})
			allowReturn := make(chan struct{})
			var releaseOnce sync.Once
			releaseProvider := func() { releaseOnce.Do(func() { close(allowReturn) }) }
			defer releaseProvider()
			session := newFakeLiveTranscriptionSession(
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionInterim, Text: "この質問を詳しく説明して", Stability: .95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionInterim, Text: "この質問を詳しく説明して", Stability: .95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionFinal, Text: test.final, Confidence: test.confidence,
				},
			)
			session.eventGates = map[int]<-chan struct{}{2: buffered}
			speech := &stalledCanceledSpeech{
				scriptedLiveSpeech: &scriptedLiveSpeech{
					session: session,
					scripts: []scriptedSynthesis{{chunks: [][]byte{{8, 0}}}},
				},
				buffered: buffered, allowReturn: allowReturn, lateCallback: make(chan error, 1),
			}
			agent := &speculativeTestAgent{
				speculativeResult: liveTestDecision("discarded reply", "spec-state"),
				normalResult:      liveTestDecision("committed reply", "normal-state"),
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
			go func() {
				result, err := pipeline.ProcessLive(ctx, "cancel-wait-test",
					httpapi.VoiceTurnInput{StateToken: "existing-state"}, audio,
					func(chunk []byte) error {
						outputMu.Lock()
						defer outputMu.Unlock()
						output = append(output, chunk...)
						return nil
					})
				done <- outcome{result, err}
			}()
			select {
			case result := <-done:
				if result.err != nil || result.result.Route != test.wantRoute ||
					result.result.StateToken != test.wantState || result.result.LiveTimings.SpecCancel != 1 {
					t.Fatalf("result=%+v err=%v", result.result, result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("committed response waited for the canceled provider")
			}
			// Only now allow the old provider to send another chunk and exit.
			releaseProvider()
			select {
			case err := <-speech.lateCallback:
				if err == nil {
					t.Fatal("canceled provider callback was accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("old provider did not return")
			}
			outputMu.Lock()
			defer outputMu.Unlock()
			if !bytes.Equal(output, []byte{8, 0}) {
				t.Fatalf("discarded PCM escaped: %v", output)
			}
		})
	}
}
