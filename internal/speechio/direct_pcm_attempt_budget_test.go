package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

func directBudgetFallbackStream() streamingSynthesizeClient {
	return &fakeStreamingSynthesizeClient{recvResults: []streamingReceiveResult{
		{response: &texttospeechpb.StreamingSynthesizeResponse{AudioContent: []byte{80, 0}}},
		{err: io.EOF},
	}}
}

func TestDirectPCMAttemptBudgetFallsBackBeforeParentDeadline(t *testing.T) {
	for _, test := range []struct {
		name            string
		lateSuccess     bool
		fallbackFailure bool
	}{
		{name: "cooperative timeout"},
		{name: "late success", lateSuccess: true},
		{name: "late success and fallback failure", lateSuccess: true, fallbackFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				started := time.Now()
				directCalls, streamCalls := 0, 0
				directReturned := false
				var attemptCtx context.Context
				fallbackErr := errors.New("stream unavailable")
				service := &CloudService{
					voiceName: "ja-JP-Chirp3-HD-Kore", streamingPCMCache: newStreamingPCMCache(8, 4096),
					synthesizePCMCall: func(
						callCtx context.Context, _ *texttospeechpb.SynthesizeSpeechRequest,
					) (*texttospeechpb.SynthesizeSpeechResponse, error) {
						directCalls++
						attemptCtx = callCtx
						<-callCtx.Done()
						directReturned = true
						if test.lateSuccess {
							return &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte{40, 0}}, nil
						}
						return nil, callCtx.Err()
					},
					streamSynthesizeCall: func(callCtx context.Context) (streamingSynthesizeClient, error) {
						streamCalls++
						if !directReturned {
							t.Error("streaming overlapped the unary provider")
						}
						// The fallback must use the parent, not the expired child.
						time.Sleep(250 * time.Millisecond)
						if err := callCtx.Err(); err != nil {
							t.Errorf("fallback inherited expired attempt context: %v", err)
						}
						if test.fallbackFailure {
							return nil, fallbackErr
						}
						return directBudgetFallbackStream(), nil
					},
				}
				const text = "short audited response"
				var output []byte
				mime, err := service.StreamSynthesize(ctx, text, func(chunk []byte) error {
					output = append(output, chunk...)
					return nil
				})
				if ctx.Err() != nil || time.Since(started) != 750*time.Millisecond {
					t.Fatalf("unary attempt consumed parent budget: elapsed=%s parent error=%v", time.Since(started), ctx.Err())
				}
				if attemptCtx == nil || !errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
					t.Fatalf("attempt context error=%v", attemptCtx)
				}
				if directCalls != 1 || streamCalls != 1 {
					t.Fatalf("direct=%d streaming=%d want=1/1", directCalls, streamCalls)
				}
				cached, cacheHit := service.streamingPCMCache.get(newStreamingPCMCacheKey(service.voiceName, text))
				if test.fallbackFailure {
					if !errors.Is(err, fallbackErr) || len(output) != 0 || cacheHit {
						t.Fatalf("late unary success escaped failed fallback: error=%v output=%v cache hit=%t", err, output, cacheHit)
					}
				} else {
					if err != nil || mime != StreamingAudioContentType || !bytes.Equal(output, []byte{80, 0}) {
						t.Fatalf("fallback result: mime=%q error=%v output=%v", mime, err, output)
					}
					if !cacheHit || len(cached.chunks) != 1 || !bytes.Equal(cached.chunks[0], []byte{80, 0}) {
						t.Fatal("cache did not retain only the successful streaming response")
					}
				}
				// A local attempt timeout is a failed optional route, not a user
				// cancellation. Following turns bypass it on this instance.
				_, _ = service.StreamSynthesize(ctx, "different short response", func([]byte) error { return nil })
				if directCalls != 1 || streamCalls != 2 {
					t.Fatalf("timed-out direct route was retried: direct=%d streaming=%d", directCalls, streamCalls)
				}
			})
		})
	}
}

func TestDirectPCMAttemptBudgetPreservesParentCancellation(t *testing.T) {
	for _, name := range []string{"cancel during attempt", "shorter parent deadline", "parent cancel with late success"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parentLimit := 5 * time.Second
				wantElapsed := 100 * time.Millisecond
				wantErr := context.Canceled
				if name == "shorter parent deadline" {
					parentLimit = wantElapsed
					wantErr = context.DeadlineExceeded
				}
				if name == "parent cancel with late success" {
					wantElapsed = 500 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(context.Background(), parentLimit)
				defer cancel()
				started := time.Now()
				directCalls, streamCalls, callbacks := 0, 0, 0
				service := &CloudService{
					voiceName: "ja-JP-Chirp3-HD-Kore", streamingPCMCache: newStreamingPCMCache(8, 4096),
					synthesizePCMCall: func(
						callCtx context.Context, _ *texttospeechpb.SynthesizeSpeechRequest,
					) (*texttospeechpb.SynthesizeSpeechResponse, error) {
						directCalls++
						if name == "cancel during attempt" {
							time.Sleep(100 * time.Millisecond)
							cancel()
						}
						<-callCtx.Done()
						if name == "parent cancel with late success" {
							cancel()
							return &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte{40, 0}}, nil
						}
						return nil, callCtx.Err()
					},
					streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
						streamCalls++
						return directBudgetFallbackStream(), nil
					},
				}
				_, err := service.StreamSynthesize(ctx, "short cancelled response", func([]byte) error {
					callbacks++
					return nil
				})
				if !errors.Is(err, wantErr) || time.Since(started) != wantElapsed {
					t.Fatalf("parent cancellation lost: error=%v elapsed=%s", err, time.Since(started))
				}
				if directCalls != 1 || streamCalls != 0 || callbacks != 0 {
					t.Fatalf("cancelled request continued: direct=%d streaming=%d callbacks=%d", directCalls, streamCalls, callbacks)
				}
				if !service.directPCMCircuit.begin() {
					t.Fatal("parent cancellation disabled the direct provider circuit")
				}
				service.directPCMCircuit.canceled()
			})
		})
	}
}

func TestDirectPCMAttemptBudgetEndsBeforeSuccessfulCallback(t *testing.T) {
	for _, callbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "slow callback", true: "callback failure"}[callbackFails], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				started := time.Now()
				var attemptCtx context.Context
				directCalls, streamCalls, callbacks := 0, 0, 0
				callbackErr := errors.New("output stopped")
				service := &CloudService{
					voiceName: "ja-JP-Chirp3-HD-Kore", streamingPCMCache: newStreamingPCMCache(8, 4096),
					synthesizePCMCall: func(
						callCtx context.Context, _ *texttospeechpb.SynthesizeSpeechRequest,
					) (*texttospeechpb.SynthesizeSpeechResponse, error) {
						directCalls++
						attemptCtx = callCtx
						time.Sleep(100 * time.Millisecond)
						return &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte{40, 0}}, nil
					},
					streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
						streamCalls++
						return directBudgetFallbackStream(), nil
					},
				}
				const text = "short timely response"
				mime, err := service.StreamSynthesize(ctx, text, func(chunk []byte) error {
					callbacks++
					if !bytes.Equal(chunk, []byte{40, 0}) {
						t.Errorf("timely direct PCM replaced: %v", chunk)
					}
					if attemptCtx == nil || !errors.Is(attemptCtx.Err(), context.Canceled) {
						t.Error("finished attempt retained its context/timer during output")
					}
					// Only provider work has the 500ms sub-budget, not delivery.
					time.Sleep(time.Second)
					if callbackFails {
						return callbackErr
					}
					return nil
				})
				if ctx.Err() != nil || time.Since(started) != 1100*time.Millisecond {
					t.Fatalf("attempt deadline interrupted output: parent=%v elapsed=%s", ctx.Err(), time.Since(started))
				}
				if directCalls != 1 || streamCalls != 0 || callbacks != 1 {
					t.Fatalf("timely output retried: direct=%d streaming=%d callbacks=%d", directCalls, streamCalls, callbacks)
				}
				_, cacheHit := service.streamingPCMCache.get(newStreamingPCMCacheKey(service.voiceName, text))
				if callbackFails {
					if !errors.Is(err, callbackErr) || cacheHit {
						t.Fatalf("failed output cached or lost error: error=%v cache hit=%t", err, cacheHit)
					}
				} else if err != nil || mime != StreamingAudioContentType || !cacheHit {
					t.Fatalf("timely direct result: mime=%q error=%v cache hit=%t", mime, err, cacheHit)
				}
				if !service.directPCMCircuit.begin() {
					t.Fatal("successful provider was disabled by output work")
				}
				service.directPCMCircuit.canceled()
			})
		})
	}
}
