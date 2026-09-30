package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

func TestCommittedSynthesisPreparedRetryBoundary(t *testing.T) {
	providerErr := errors.New("idle connection expired")
	callbackErr := errors.New("output rejected")
	for _, test := range []struct {
		name                     string
		chunks                   [][]byte
		providerErr, callbackErr error
		wantFallback             int
		wantErr                  error
	}{
		{"ready", [][]byte{{1, 0}}, nil, nil, 0, nil},
		{"expired before PCM", nil, providerErr, nil, 1, nil},
		{"partial PCM", [][]byte{{1, 0}}, providerErr, nil, 0, providerErr},
		{"silent PCM", [][]byte{{0, 0}}, providerErr, nil, 0, providerErr},
		{"callback failure", [][]byte{{1, 0}}, nil, callbackErr, 0, callbackErr},
		{"empty callback failure", [][]byte{nil}, nil, callbackErr, 0, callbackErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakePreparedSynthesis{chunks: test.chunks, err: test.providerErr}
			preparation := &speculativeSynthesisPreparation{prepared: provider}
			defer preparation.close()
			speech := &fakeStreamingSpeech{chunks: [][]byte{{9, 0}}}
			var output []byte
			mime, err := streamCommittedSynthesis(context.Background(), speech, preparation, "audited reply", func(chunk []byte) error {
				if test.callbackErr != nil {
					return test.callbackErr
				}
				output = append(output, chunk...)
				return nil
			})
			if !errors.Is(err, test.wantErr) || (err == nil && mime != speechio.StreamingAudioContentType) {
				t.Fatalf("mime=%q err=%v want=%v", mime, err, test.wantErr)
			}
			if provider.calls != 1 || !provider.closed || speech.streamCalls != test.wantFallback || preparation.takeReady() != nil {
				t.Fatalf("prepared=%d closed=%t fallback=%d", provider.calls, provider.closed, speech.streamCalls)
			}
			if test.wantFallback == 1 && !bytes.Equal(output, []byte{9, 0}) {
				t.Fatalf("fallback output=%v", output)
			}
		})
	}
}

func TestCommittedSynthesisNeverWaitsForPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		speech := &preparingStreamingSpeech{
			fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{9, 0}}},
			blockPrepare:        true, prepareStarted: make(chan struct{}), prepareCanceled: make(chan struct{}),
		}
		preparation := startSpeculativeSynthesisPreparation(context.Background(), speech)
		defer preparation.close()
		<-speech.prepareStarted
		done := make(chan error, 1)
		go func() {
			_, err := streamCommittedSynthesis(context.Background(), speech, preparation, "reply", func([]byte) error { return nil })
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil || speech.streamCalls != 1 {
				t.Fatalf("err=%v streams=%d", err, speech.streamCalls)
			}
		default:
			t.Fatal("committed synthesis waited for preparation")
		}
		preparation.close()
		<-speech.prepareCanceled
	})
}

func TestCommittedSynthesisCancellationClosesOwnedProvider(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "already canceled"}[alreadyCanceled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := context.Background()
				providerCtx, cancelProvider := context.WithCancel(parent)
				provider := &contextBlockedPreparedSpeech{ctx: providerCtx, started: make(chan struct{}), closed: make(chan struct{})}
				preparation := &speculativeSynthesisPreparation{prepared: provider, cancel: cancelProvider}
				defer preparation.close()
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				if alreadyCanceled {
					cancel()
				}
				speech := &fakeStreamingSpeech{}
				done := make(chan error, 1)
				go func() {
					_, err := streamCommittedSynthesis(ctx, speech, preparation, "reply", func([]byte) error { t.Error("unexpected audio"); return nil })
					done <- err
				}()
				if !alreadyCanceled {
					<-provider.started
					cancel()
				}
				synctest.Wait()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("err=%v", err)
					}
				default:
					t.Fatal("cancellation left committed synthesis blocked")
				}
				preparation.close()
				<-provider.closed
				wantCalls := int32(1)
				if alreadyCanceled {
					wantCalls = 0
				}
				if provider.calls.Load() != wantCalls || speech.streamCalls != 0 || parent.Err() != nil {
					t.Fatalf("provider=%d fallback=%d parent=%v", provider.calls.Load(), speech.streamCalls, parent.Err())
				}
			})
		})
	}
}

func TestLiveFinalOnlyReusesPreparedConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &fakePreparedSynthesis{chunks: [][]byte{{8, 0}}}
		speech := &preparingLiveSpeech{
			preparingStreamingSpeech: preparingStreamingSpeech{
				fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{9, 0}}}, prepared: provider,
			},
			session: newFakeLiveTranscriptionSession(speechio.StreamingTranscriptionEvent{
				Kind: speechio.StreamingTranscriptionFinal, Text: "こんにちは", Confidence: .98,
			}),
		}
		agent := &fakeAgent{result: liveTestDecision("こんにちは。", "new-state")}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		audio := make(chan []byte, 1)
		done := make(chan error, 1)
		var output []byte
		go func() {
			result, err := pipeline.ProcessLive(context.Background(), "final-only", httpapi.VoiceTurnInput{}, audio,
				func(chunk []byte) error { output = append(output, chunk...); return nil })
			if err == nil && result.Caption != "こんにちは。" {
				t.Errorf("caption=%q", result.Caption)
			}
			done <- err
		}()
		// Let configuration finish while the person is still speaking. Waiting
		// for quiescence makes readiness deterministic without a real sleep.
		synctest.Wait()
		audio <- []byte{1, 0}
		close(audio)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if provider.calls != 1 || speech.streamCalls != 0 || agent.calls != 1 || agent.turn.Speculative || !bytes.Equal(output, []byte{8, 0}) {
			t.Fatalf("prepared=%d fallback=%d agent=%d speculative=%t output=%v", provider.calls, speech.streamCalls, agent.calls, agent.turn.Speculative, output)
		}
	})
}
