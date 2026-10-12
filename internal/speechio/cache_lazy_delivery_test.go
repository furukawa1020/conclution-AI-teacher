package speechio

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestCachedDeliveryOwnsEachChunkAcrossReplacement(t *testing.T) {
	for _, route := range []string{"ordinary", "prepared", "shared owner"} {
		t.Run(route, func(t *testing.T) {
			service := cloudServiceWithStream(&fakeStreamingSynthesizeClient{})
			service.streamingPCMCache = newStreamingPCMCache(1, 1024)
			key := newStreamingPCMCacheKey(service.voiceName, "reply")
			original := cachedStreamingPCM{chunks: [][]byte{{80, 0}, {81, 0}}, size: 4}
			service.streamingPCMCache.put(key, original)
			var received []byte
			calls := 0
			handler := func(chunk []byte) error {
				received = append(received, chunk...)
				clear(chunk)
				stored, _ := service.streamingPCMCache.get(key)
				if calls == 0 {
					if !bytes.Equal(stored.chunks[0], original.chunks[0]) {
						t.Fatal("callback mutated cache-owned PCM")
					}
					// A callback may reenter the cache; delivery must not hold its lock.
					service.streamingPCMCache.put(key, cachedStreamingPCM{chunks: [][]byte{{90, 0}}, size: 2})
					service.streamingPCMCache.put(newStreamingPCMCacheKey("voice", "other"), original)
				}
				calls++
				return nil
			}
			var mime string
			var err error
			switch route {
			case "ordinary":
				mime, err = service.StreamSynthesize(context.Background(), "reply", handler)
			case "prepared":
				prepared, prepareErr := service.PrepareStreamingSynthesis(context.Background())
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				defer prepared.Close()
				mime, err = prepared.StreamSynthesize("reply", handler)
			case "shared owner":
				mime, err = service.streamSynthesizeSharedOwner(context.Background(), "reply", handler)
			}
			if err != nil || mime != StreamingAudioContentType || !bytes.Equal(received, []byte{80, 0, 81, 0}) {
				t.Fatalf("mime=%q err=%v audio=%v", mime, err, received)
			}
		})
	}
}

func TestCachedDeliveryStopsWithoutExposingTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entry := cachedStreamingPCM{chunks: [][]byte{{80, 0}, {81, 0}}, size: 4}
	calls := 0
	err := deliverCachedStreamingPCM(ctx, entry, func(chunk []byte) error {
		calls++
		clear(chunk)
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 || entry.chunks[0][0] != 80 {
		t.Fatalf("err=%v calls=%d original=%v", err, calls, entry.chunks)
	}
}

func BenchmarkCachedDeliveryStopAfterFirstChunk(b *testing.B) {
	cache := newStreamingPCMCache(1, 1<<20)
	key := newStreamingPCMCacheKey("voice", "reply")
	entry := cachedStreamingPCM{size: 1 << 20}
	for range 128 {
		entry.chunks = append(entry.chunks, make([]byte, 8192))
	}
	cache.put(key, entry)
	stop := errors.New("subscriber stopped")
	for _, eager := range []bool{true, false} {
		b.Run(map[bool]string{true: "eager", false: "incremental"}[eager], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if eager {
					pcm, _ := cache.get(key)
					// Previous delivery transferred the already-owned first chunk.
					_ = benchmarkStopChunk(pcm.chunks[0], stop)
				} else {
					pcm, _ := cache.snapshot(key)
					_ = deliverCachedStreamingPCM(context.Background(), pcm, func(chunk []byte) error {
						return benchmarkStopChunk(chunk, stop)
					})
				}
			}
		})
	}
}

//go:noinline
func benchmarkStopChunk(chunk []byte, err error) error {
	clear(chunk)
	return err
}
