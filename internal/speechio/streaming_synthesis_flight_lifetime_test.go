package speechio

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestStreamingSynthesisFlightCanceledAdmissionDoesNotStartProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var group streamingSynthesisFlightGroup
		key := newStreamingPCMCacheKey("voice", "audited cue")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		started, done := make(chan struct{}), make(chan error, 1)
		// The registration lock cannot be acquired until after cancellation.
		group.mu.Lock()
		go func() {
			close(started)
			_, err := group.stream(ctx, key,
				func(context.Context, StreamChunkHandler) (string, error) {
					calls.Add(1)
					return StreamingAudioContentType, nil
				}, func([]byte) error { return nil })
			done <- err
		}()
		<-started
		cancel()
		group.mu.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("admission error=%v", err)
		}
		synctest.Wait()
		if calls.Load() != 0 || group.flights != nil {
			t.Fatalf("canceled admission initialized registry or started provider: calls=%d", calls.Load())
		}
	})
}

func TestStreamingSynthesisFlightLastSubscriberReleasesPCMBeforeProviderReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var group streamingSynthesisFlightGroup
		key := newStreamingPCMCacheKey("voice", "audited cue")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		releaseProvider, releaseCallback := make(chan struct{}), make(chan struct{})
		var providerOnce, callbackOnce sync.Once
		finishProvider := func() { providerOnce.Do(func() { close(releaseProvider) }) }
		finishCallback := func() { callbackOnce.Do(func() { close(releaseCallback) }) }
		defer finishProvider()
		defer finishCallback()
		var providerCtx context.Context
		var received []byte
		done := make(chan error, 1)
		go func() {
			_, err := group.stream(ctx, key,
				func(callCtx context.Context, publish StreamChunkHandler) (string, error) {
					providerCtx = callCtx
					if err := publish([]byte{80, 0}); err != nil {
						return "", err
					}
					// Simulate a provider whose shutdown takes longer than the caller's.
					<-releaseProvider
					return "", callCtx.Err()
				}, func(chunk []byte) error {
					received = chunk
					<-releaseCallback
					return nil
				})
			done <- err
		}()
		synctest.Wait()
		group.mu.Lock()
		flight := group.flights[key]
		group.mu.Unlock()
		if flight == nil || providerCtx == nil || !bytes.Equal(received, []byte{80, 0}) {
			t.Fatal("shared stream did not publish before cancellation")
		}
		flight.mu.Lock()
		retained := flight.chunks[0]
		flight.mu.Unlock()
		cancel()
		finishCallback()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("subscriber error=%v", err)
		}
		flight.mu.Lock()
		if flight.done || flight.subscribers != 0 || flight.chunks != nil || flight.buffered != 0 {
			t.Error("abandoned flight retained PCM while provider was still stopping")
		}
		if !bytes.Equal(retained, []byte{0, 0}) || !bytes.Equal(received, []byte{80, 0}) {
			t.Error("internal PCM was not cleared or caller-owned copy was changed")
		}
		flight.mu.Unlock()
		if providerCtx.Err() != context.Canceled {
			t.Error("last subscriber did not cancel provider")
		}
		if err := flight.publish([]byte{81, 0}); !errors.Is(err, context.Canceled) {
			t.Errorf("abandoned flight accepted late PCM: %v", err)
		}
		finishProvider()
		synctest.Wait()
		if len(group.flights) != 0 {
			t.Fatal("finished provider remained registered")
		}
	})
}

func TestStreamingSynthesisFlightReleaseKeepsRemainingSubscriberPCM(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider active", true: "provider complete"}[completed], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			flight := &streamingSynthesisFlight{subscribers: 2, cancel: cancel}
			flight.changed = sync.NewCond(&flight.mu)
			if err := flight.publish([]byte{80, 0}); err != nil {
				t.Fatal(err)
			}
			retained := flight.chunks[0]
			if completed {
				flight.complete(StreamingAudioContentType, nil)
			}
			flight.unsubscribe()
			if flight.subscribers != 1 || flight.buffered != 2 || len(flight.chunks) != 1 ||
				!bytes.Equal(retained, []byte{80, 0}) || (!completed && ctx.Err() != nil) {
				t.Fatal("one subscriber leaving destroyed the remaining subscriber's stream")
			}
			flight.unsubscribe()
			if flight.chunks != nil || flight.buffered != 0 || !bytes.Equal(retained, []byte{0, 0}) {
				t.Fatal("last subscriber left internal PCM retained")
			}
			if ctx.Err() != context.Canceled {
				t.Fatal("provider still active after last subscriber left")
			}
		})
	}
}
