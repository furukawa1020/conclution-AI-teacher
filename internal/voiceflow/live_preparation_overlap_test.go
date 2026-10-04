package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type openingPreparedLiveSpeech struct {
	preparingLiveSpeech
	openGate         <-chan struct{}
	latePreparedGate <-chan struct{}
	openErr          error
	preparationErr   error
	openCalls        atomic.Int64
	preparationCalls atomic.Int64
}

func (speech *openingPreparedLiveSpeech) OpenStreamingTranscription(ctx context.Context) (speechio.StreamingTranscriptionSession, error) {
	speech.openCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-speech.openGate:
	}
	if speech.openErr != nil {
		return nil, speech.openErr
	}
	return speech.preparingLiveSpeech.OpenStreamingTranscription(ctx)
}

func (speech *openingPreparedLiveSpeech) PrepareStreamingSynthesis(ctx context.Context) (speechio.PreparedStreamingSynthesis, error) {
	speech.preparationCalls.Add(1)
	if speech.preparationErr != nil {
		speech.startOnce.Do(func() { close(speech.prepareStarted) })
		return nil, speech.preparationErr
	}
	if speech.latePreparedGate != nil {
		speech.startOnce.Do(func() { close(speech.prepareStarted) })
		// Deliberately return a capability after cancellation. The turn must
		// have finished independently, and the late owner must be closed.
		<-speech.latePreparedGate
		speech.prepared.mu.Lock()
		speech.prepared.ctx = ctx
		speech.prepared.mu.Unlock()
		return speech.prepared, nil
	}
	return speech.preparingLiveSpeech.PrepareStreamingSynthesis(ctx)
}

func TestLivePreparationOverlapsRecognizerOpen(t *testing.T) {
	for _, test := range []struct {
		name                                                                        string
		slowPreparation, failedPreparation, failedOpen, cancelOpen, latePreparation bool
	}{
		{name: "ready capability is reused"},
		{name: "slow preparation never delays response", slowPreparation: true},
		{name: "failed preparation keeps ordinary synthesis", failedPreparation: true},
		{name: "recognizer open failure closes preparation", failedOpen: true},
		{name: "parent cancellation closes preparation", cancelOpen: true},
		{name: "late capability after open failure", failedOpen: true, latePreparation: true},
		{name: "late capability after cancellation", cancelOpen: true, latePreparation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				openGate, lateGate := make(chan struct{}), make(chan struct{})
				var openOnce, lateOnce sync.Once
				releaseOpen := func() { openOnce.Do(func() { close(openGate) }) }
				releaseLate := func() { lateOnce.Do(func() { close(lateGate) }) }
				defer releaseOpen()
				defer releaseLate()
				prepared := &fakePreparedSynthesis{chunks: [][]byte{{80, 0}}}
				speech := &openingPreparedLiveSpeech{
					preparingLiveSpeech: preparingLiveSpeech{
						preparingStreamingSpeech: preparingStreamingSpeech{
							fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{90, 0}}},
							prepared:            prepared, prepareStarted: make(chan struct{}),
							prepareCanceled: make(chan struct{}), blockPrepare: test.slowPreparation,
						},
						session: newFakeLiveTranscriptionSession(speechio.StreamingTranscriptionEvent{
							Kind: speechio.StreamingTranscriptionFinal, Text: "こんにちは", Confidence: .99,
						}),
					},
					openGate: openGate,
				}
				if test.failedOpen {
					speech.openErr = errors.New("recognizer unavailable")
				}
				if test.failedPreparation {
					speech.preparationErr = errors.New("configuration unavailable")
				}
				if test.latePreparation {
					speech.latePreparedGate = lateGate
				}
				agent := &fakeAgent{result: liveTestDecision("こんにちは。", "final-state")}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				audio := make(chan []byte, 1)
				audio <- []byte{1, 0}
				close(audio)
				var output []byte
				done := make(chan error, 1)
				go func() {
					_, err := pipeline.ProcessLive(ctx, "open-overlap", httpapi.VoiceTurnInput{}, audio, func(chunk []byte) error { output = append(output, chunk...); return nil })
					done <- err
				}()
				synctest.Wait()
				if speech.openCalls.Load() != 1 || speech.preparationCalls.Load() != 1 {
					t.Fatalf("configuration did not overlap recognizer Open: recognition=%d preparation=%d", speech.openCalls.Load(), speech.preparationCalls.Load())
				}
				if agent.calls != 0 || prepared.calls != 0 || speech.streamCalls != 0 || len(output) != 0 {
					t.Fatal("content crossed configuration-only preparation while recognizer Open was blocked")
				}
				if test.cancelOpen {
					cancel()
				}
				releaseOpen()
				synctest.Wait()
				select {
				case err = <-done:
				default:
					t.Fatal("live turn waited for unfinished TTS configuration")
				}
				failed := test.failedOpen || test.cancelOpen
				if (err != nil) != failed {
					t.Fatalf("turn error=%v", err)
				}
				if failed {
					if agent.calls != 0 || prepared.calls != 0 || speech.streamCalls != 0 || len(output) != 0 {
						t.Fatal("failed recognizer path synthesized a reply")
					}
				} else {
					wantPCM, wantPrepared, wantOrdinary := []byte{80, 0}, 1, 0
					if test.slowPreparation || test.failedPreparation {
						wantPCM, wantPrepared, wantOrdinary = []byte{90, 0}, 0, 1
					}
					if !bytes.Equal(output, wantPCM) || prepared.calls != wantPrepared || speech.streamCalls != wantOrdinary || agent.calls != 1 {
						t.Fatalf("output=%v prepared=%d ordinary=%d model=%d", output, prepared.calls, speech.streamCalls, agent.calls)
					}
				}
				if test.latePreparation {
					if prepared.closed {
						t.Fatal("test capability returned before late gate")
					}
					releaseLate()
					synctest.Wait()
				}
				if test.slowPreparation {
					select {
					case <-speech.prepareCanceled:
					default:
						t.Fatal("unused pending configuration was not canceled")
					}
				} else if !test.failedPreparation && (!prepared.closed || prepared.ctx == nil || prepared.ctx.Err() == nil) {
					t.Fatal("unused or consumed preparation was not reclaimed")
				}
				if speech.preparationCalls.Load() != 1 {
					t.Fatal("configuration preparation was repeated")
				}
			})
		})
	}
}

func TestLivePreparationSkipsIneligibleOrCanceledInput(t *testing.T) {
	for _, test := range []struct {
		name     string
		input    httpapi.VoiceTurnInput
		canceled bool
	}{
		{name: "strict", input: httpapi.VoiceTurnInput{StrictCloudMinimization: true}},
		{name: "document", input: httpapi.VoiceTurnInput{Document: &httpapi.VoiceDocument{Data: []byte("private")}}},
		{name: "passive ambient", input: httpapi.VoiceTurnInput{Ambient: true}},
		{name: "already canceled", canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test.canceled {
					cancel()
				}
				gate := make(chan struct{})
				close(gate)
				speech := &openingPreparedLiveSpeech{openGate: gate, openErr: errors.New("stop at recognizer Open")}
				pipeline, err := New(speech, &fakeAgent{})
				if err != nil {
					t.Fatal(err)
				}
				audio := make(chan []byte)
				close(audio)
				_, _ = pipeline.ProcessLive(ctx, "excluded-open", test.input, audio, func([]byte) error { t.Fatal("excluded turn delivered audio"); return nil })
				synctest.Wait()
				if speech.preparationCalls.Load() != 0 {
					t.Fatal("ineligible or canceled turn started TTS preparation")
				}
			})
		})
	}
}
