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

func TestStreamingSynthesisFlightDoesNotReplayCompletedResult(t *testing.T) {
	for _, test := range []struct {
		name        string
		oldAudio    []byte
		oldErr      error
		subscribers int
	}{
		{name: "failed before PCM", oldErr: errors.New("previous provider failed")},
		{name: "successful with subscriber still draining", oldAudio: []byte{40, 0}, subscribers: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Completion wakes callers before the producer removes its map
				// entry. Seed that exact state so retry behavior is deterministic
				// rather than depending on which goroutine wins the group mutex.
				oldCtx, cancelOld := context.WithCancel(context.Background())
				defer cancelOld()
				oldFlight := &streamingSynthesisFlight{
					cancel: cancelOld, subscribers: test.subscribers,
				}
				oldFlight.changed = sync.NewCond(&oldFlight.mu)
				if len(test.oldAudio) > 0 {
					if err := oldFlight.publish(test.oldAudio); err != nil {
						t.Fatal(err)
					}
				}
				oldFlight.complete(StreamingAudioContentType, test.oldErr)
				if oldCtx.Err() != context.Canceled {
					t.Fatal("completed fixture still owns an active provider")
				}
				key := newStreamingPCMCacheKey("voice", "audited fixed reply")
				group := streamingSynthesisFlightGroup{
					flights: map[streamingPCMCacheKey]*streamingSynthesisFlight{key: oldFlight},
				}
				var providerCalls atomic.Int32
				freshAudio := []byte{80, 0}
				var received []byte
				mime, err := group.stream(context.Background(), key,
					func(_ context.Context, publish StreamChunkHandler) (string, error) {
						providerCalls.Add(1)
						if err := publish(freshAudio); err != nil {
							return "", err
						}
						return StreamingAudioContentType, nil
					},
					func(chunk []byte) error {
						received = append(received, chunk...)
						return nil
					},
				)
				if err != nil || mime != StreamingAudioContentType ||
					providerCalls.Load() != 1 || !bytes.Equal(received, freshAudio) {
					t.Fatalf("completed result was reused: provider calls=%d mime=%q error=%v audio=%v",
						providerCalls.Load(), mime, err, received)
				}
				if test.subscribers > 0 {
					var oldReceived []byte
					oldMIME, oldErr := oldFlight.consume(context.Background(), func(chunk []byte) error {
						oldReceived = append(oldReceived, chunk...)
						return nil
					})
					if !errors.Is(oldErr, test.oldErr) || oldMIME != StreamingAudioContentType ||
						!bytes.Equal(oldReceived, test.oldAudio) {
						t.Fatalf("replacement changed an existing subscriber's result: mime=%q error=%v audio=%v",
							oldMIME, oldErr, oldReceived)
					}
				}
				synctest.Wait()
				group.mu.Lock()
				remaining := len(group.flights)
				group.mu.Unlock()
				if remaining != 0 {
					t.Fatalf("replacement left %d completed flights registered", remaining)
				}
			})
		})
	}
}

func TestStreamingSynthesisFlightOldCleanupPreservesReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var group streamingSynthesisFlightGroup
		key := newStreamingPCMCacheKey("voice", "audited fixed reply")
		releaseOld := make(chan struct{})
		var releaseOldOnce sync.Once
		finishOld := func() { releaseOldOnce.Do(func() { close(releaseOld) }) }
		defer finishOld()
		releaseReplacement := make(chan struct{})
		var releaseReplacementOnce sync.Once
		finishReplacement := func() {
			releaseReplacementOnce.Do(func() { close(releaseReplacement) })
		}
		defer finishReplacement()
		var providerCalls atomic.Int32
		produce := func(ctx context.Context, publish StreamChunkHandler) (string, error) {
			switch providerCalls.Add(1) {
			case 1:
				// Delay provider shutdown after its only subscriber leaves, so
				// a replacement is registered before the old owner cleans up.
				<-releaseOld
				return "", ctx.Err()
			case 2:
				<-releaseReplacement
				if err := publish([]byte{80, 0}); err != nil {
					return "", err
				}
				return StreamingAudioContentType, nil
			default:
				return "", errors.New("replacement was incorrectly removed")
			}
		}
		type result struct {
			audio []byte
			err   error
		}
		consume := func(ctx context.Context, done chan<- result) {
			var outcome result
			_, outcome.err = group.stream(ctx, key, produce, func(chunk []byte) error {
				outcome.audio = append(outcome.audio, chunk...)
				return nil
			})
			done <- outcome
		}
		oldCtx, cancelOld := context.WithCancel(context.Background())
		defer cancelOld()
		oldDone := make(chan result, 1)
		go consume(oldCtx, oldDone)
		synctest.Wait()
		group.mu.Lock()
		oldFlight := group.flights[key]
		group.mu.Unlock()
		if oldFlight == nil || providerCalls.Load() != 1 {
			t.Fatal("old provider did not start")
		}
		cancelOld()
		synctest.Wait()
		select {
		case outcome := <-oldDone:
			if !errors.Is(outcome.err, context.Canceled) {
				t.Fatalf("abandoned subscriber error=%v", outcome.err)
			}
		default:
			t.Fatal("canceled subscriber did not leave the old flight")
		}

		replacementDone := make(chan result, 2)
		go consume(context.Background(), replacementDone)
		synctest.Wait()
		group.mu.Lock()
		replacement := group.flights[key]
		group.mu.Unlock()
		if replacement == nil || replacement == oldFlight || providerCalls.Load() != 2 {
			t.Fatal("abandoned provider was not replaced")
		}
		finishOld()
		synctest.Wait()
		group.mu.Lock()
		current := group.flights[key]
		group.mu.Unlock()
		if current != replacement {
			t.Fatal("old producer cleanup removed the active replacement")
		}
		go consume(context.Background(), replacementDone)
		synctest.Wait()
		if providerCalls.Load() != 2 {
			t.Fatalf("late subscriber started provider %d instead of joining replacement", providerCalls.Load())
		}
		finishReplacement()
		synctest.Wait()
		for range 2 {
			select {
			case outcome := <-replacementDone:
				if outcome.err != nil || !bytes.Equal(outcome.audio, []byte{80, 0}) {
					t.Fatalf("replacement outcome error=%v audio=%v", outcome.err, outcome.audio)
				}
			default:
				t.Fatal("replacement subscriber did not finish")
			}
		}
		group.mu.Lock()
		remaining := len(group.flights)
		group.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("completed replacement left %d flights registered", remaining)
		}
	})
}
