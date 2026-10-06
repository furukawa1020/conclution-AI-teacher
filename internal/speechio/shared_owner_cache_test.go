package speechio

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

func TestSharedSynthesisOwnerRechecksCacheAfterInitialMiss(t *testing.T) {
	const text = "audited fixed reply"
	var providerCalls atomic.Int32
	service := &CloudService{
		voiceName:         "ja-JP-Chirp3-HD-Kore",
		streamingPCMCache: newStreamingPCMCache(2, 1024),
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			providerCalls.Add(1)
			return nil, errors.New("ready PCM must not reconnect")
		},
	}
	key := newStreamingPCMCacheKey(service.voiceName, text)
	service.warmSynthesisKeys = map[streamingPCMCacheKey]struct{}{key: {}}
	// A completed owner can remain registered before its cleanup acquires the
	// group mutex. It must still be replaced, not replayed as a live flight.
	oldFlight := &streamingSynthesisFlight{done: true}
	oldFlight.changed = sync.NewCond(&oldFlight.mu)
	service.synthesisFlights.flights = map[streamingPCMCacheKey]*streamingSynthesisFlight{
		key: oldFlight,
	}
	oldFlight.mu.Lock()
	var unlockOnce sync.Once
	unlockFlight := func() { unlockOnce.Do(oldFlight.mu.Unlock) }
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		mime  string
		audio []byte
		err   error
	}
	var outcome result
	done := make(chan struct{})
	go func() {
		defer close(done)
		outcome.mime, outcome.err = service.StreamSynthesize(ctx, text, func(chunk []byte) error {
			outcome.audio = append(outcome.audio, chunk...)
			return nil
		})
	}()
	defer func() {
		cancel()
		unlockFlight()
		<-done
	}()

	// Only the caller above can hold group.mu. With oldFlight.mu held here,
	// observing its group lock proves it passed the outer cache miss and is
	// inspecting that old flight. No scheduling delay is used as evidence.
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for service.synthesisFlights.mu.TryLock() {
		service.synthesisFlights.mu.Unlock()
		select {
		case <-done:
			t.Fatal("caller returned before inspecting the registered flight")
		case <-timeout.C:
			t.Fatal("caller did not reach the registered flight")
		default:
			runtime.Gosched()
		}
	}
	service.streamingPCMCache.put(key, cachedStreamingPCM{
		chunks: [][]byte{{40, 0}, {41, 0}}, size: 4,
	})
	unlockFlight()
	select {
	case <-done:
	case <-timeout.C:
		t.Fatal("caller did not complete after the successful cache refill")
	}
	if outcome.err != nil || outcome.mime != StreamingAudioContentType ||
		!bytes.Equal(outcome.audio, []byte{40, 0, 41, 0}) || providerCalls.Load() != 0 {
		t.Fatalf("cache refill was not reused: mime=%q error=%v PCM=%v provider calls=%d",
			outcome.mime, outcome.err, outcome.audio, providerCalls.Load())
	}
	// Completion wakes the subscriber before the owner removes its registry
	// entry. Observe that cleanup separately; taking the mutex once is no join.
	for {
		service.synthesisFlights.mu.Lock()
		remaining := len(service.synthesisFlights.flights)
		service.synthesisFlights.mu.Unlock()
		if remaining == 0 {
			break
		}
		select {
		case <-timeout.C:
			t.Fatal("completed shared owner did not leave the registry")
		default:
			runtime.Gosched()
		}
	}
}

func TestSharedSynthesisOwnerCachedDeliveryBoundaries(t *testing.T) {
	callbackFailure := errors.New("cached playback rejected")
	for _, test := range []struct {
		name          string
		cancelBefore  bool
		cancelOnFirst bool
		callbackError error
		wantChunks    int
		wantError     error
	}{
		{name: "complete", wantChunks: 2},
		{name: "canceled before delivery", cancelBefore: true, wantError: context.Canceled},
		{name: "canceled between chunks", cancelOnFirst: true, wantChunks: 1, wantError: context.Canceled},
		{name: "callback failure", callbackError: callbackFailure, wantChunks: 1, wantError: callbackFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			providerCalls := 0
			service := &CloudService{
				voiceName:         "ja-JP-Chirp3-HD-Kore",
				streamingPCMCache: newStreamingPCMCache(2, 1024),
				streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
					providerCalls++
					return nil, errors.New("cached delivery must not open a provider")
				},
			}
			const text = "audited fixed reply"
			key := newStreamingPCMCacheKey(service.voiceName, text)
			chunks := [][]byte{{40, 0}, {41, 0}}
			service.streamingPCMCache.put(key, cachedStreamingPCM{chunks: chunks, size: 4})
			if test.cancelBefore {
				cancel()
			}
			var received [][]byte
			mime, err := service.streamSynthesizeSharedOwner(ctx, text, func(chunk []byte) error {
				received = append(received, append([]byte(nil), chunk...))
				// A callback owns only its delivery copy, never the cached asset.
				clear(chunk)
				if test.cancelOnFirst {
					cancel()
				}
				return test.callbackError
			})
			if !errors.Is(err, test.wantError) || len(received) != test.wantChunks || providerCalls != 0 {
				t.Fatalf("error=%v chunks=%d provider calls=%d", err, len(received), providerCalls)
			}
			if (err == nil && mime != StreamingAudioContentType) || (err != nil && mime != "") {
				t.Fatalf("mime=%q error=%v", mime, err)
			}
			for index, chunk := range received {
				if !bytes.Equal(chunk, chunks[index]) {
					t.Fatalf("cached chunk %d changed: %v", index, chunk)
				}
			}
			cached, ok := service.streamingPCMCache.get(key)
			if !ok || len(cached.chunks) != len(chunks) {
				t.Fatal("delivery removed the successful cached asset")
			}
			for index, chunk := range cached.chunks {
				if !bytes.Equal(chunk, chunks[index]) {
					t.Fatalf("callback mutated cached chunk %d: %v", index, chunk)
				}
			}
		})
	}
}

func TestSharedSynthesisOwnerMissPreservesProviderPath(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			name := "nil cache"
			if cacheEnabled {
				name = "different cache keys"
			}
			if canceled {
				name += " canceled"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stream := &fakeStreamingSynthesizeClient{recvResults: []streamingReceiveResult{
					{response: &texttospeechpb.StreamingSynthesizeResponse{AudioContent: []byte{42, 0}}},
				}}
				providerCalls := 0
				service := &CloudService{
					voiceName: "ja-JP-Chirp3-HD-Kore",
					streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
						providerCalls++
						return stream, nil
					},
				}
				const text = "audited fixed reply"
				if cacheEnabled {
					service.streamingPCMCache = newStreamingPCMCache(4, 1024)
					other := cachedStreamingPCM{chunks: [][]byte{{90, 0}}, size: 2}
					service.streamingPCMCache.put(newStreamingPCMCacheKey("other voice", text), other)
					service.streamingPCMCache.put(newStreamingPCMCacheKey(service.voiceName, "other reply"), other)
				}
				if canceled {
					cancel()
				}
				var audio []byte
				mime, err := service.streamSynthesizeSharedOwner(ctx, text, func(chunk []byte) error {
					audio = append(audio, chunk...)
					return nil
				})
				if canceled {
					if !errors.Is(err, context.Canceled) || mime != "" || len(audio) != 0 || providerCalls != 0 {
						t.Fatalf("canceled owner continued: mime=%q error=%v PCM=%v provider calls=%d", mime, err, audio, providerCalls)
					}
					return
				}
				if err != nil || mime != StreamingAudioContentType || !bytes.Equal(audio, []byte{42, 0}) || providerCalls != 1 {
					t.Fatalf("miss changed provider route: mime=%q error=%v PCM=%v provider calls=%d", mime, err, audio, providerCalls)
				}
				if len(stream.sent) != 2 || stream.sent[1].GetInput().GetText() != text || !stream.closeCalled {
					t.Fatal("miss did not preserve the configuration, exact reply and input close")
				}
				if cacheEnabled {
					cached, ok := service.streamingPCMCache.get(newStreamingPCMCacheKey(service.voiceName, text))
					if !ok || len(cached.chunks) != 1 || !bytes.Equal(cached.chunks[0], audio) {
						t.Fatal("successful provider audio was not cached")
					}
				}
			})
		}
	}
}
