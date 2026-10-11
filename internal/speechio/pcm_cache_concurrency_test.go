package speechio

import (
	"bytes"
	"sync"
	"testing"
)

func TestPCMCacheConcurrentCopiesSurviveReplacementAndEviction(t *testing.T) {
	const size = 16 << 10
	cache := newStreamingPCMCache(1, size)
	key := newStreamingPCMCacheKey("voice", "reply")
	other := newStreamingPCMCacheKey("voice", "other")
	put := func(key streamingPCMCacheKey, value byte) {
		input := bytes.Repeat([]byte{value}, size)
		cache.put(key, cachedStreamingPCM{chunks: [][]byte{input[:size/2], input[size/2:]}, size: size})
		clear(input) // put must own its copy before returning.
	}
	put(key, 1)
	held, ok := cache.get(key)
	if !ok {
		t.Fatal("missing initial entry")
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for n := 0; n < 128; n++ {
				entry, found := cache.get(key)
				if !found {
					continue
				} // eviction is allowed, torn copies are not.
				if entry.size != size || len(entry.chunks) != 2 {
					t.Error("invalid snapshot")
					return
				}
				value := entry.chunks[0][0]
				if value != 1 && value != 2 {
					t.Error("copy contaminated")
					return
				}
				for _, chunk := range entry.chunks {
					if !bytes.Equal(chunk, bytes.Repeat([]byte{value}, size/2)) {
						t.Error("mixed snapshot")
						return
					}
					clear(chunk) // a reader must not mutate another reader or cache.
				}
			}
		}()
	}
	close(start)
	for n := 0; n < 128; n++ {
		put(key, byte(1+n%2))
		put(other, 3)
	}
	workers.Wait()
	put(key, 2)
	final, ok := cache.get(key)
	if !ok || final.chunks[0][0] != 2 {
		t.Fatal("final entry corrupted")
	}
	for _, chunk := range held.chunks {
		if !bytes.Equal(chunk, bytes.Repeat([]byte{1}, size/2)) {
			t.Fatal("held copy changed after eviction")
		}
	}
}

// Keep the previous implementation as a separate function, including its
// deferred unlock, so the comparison has the same return/escape boundary.
func lockedPCMCacheGet(cache *streamingPCMCache, key streamingPCMCacheKey) (cachedStreamingPCM, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		return cachedStreamingPCM{}, false
	}
	cache.removeFromOrder(key)
	cache.order = append(cache.order, key)
	return cloneCachedStreamingPCM(entry), true
}

func BenchmarkPCMCacheParallelCopy(b *testing.B) {
	for _, locked := range []bool{true, false} {
		b.Run(map[bool]string{true: "locked", false: "snapshot"}[locked], func(b *testing.B) {
			const size = 256 << 10
			cache := newStreamingPCMCache(1, size)
			key := newStreamingPCMCacheKey("voice", "fixed cue")
			cache.put(key, cachedStreamingPCM{chunks: [][]byte{bytes.Repeat([]byte{1}, size)}, size: size})
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					var entry cachedStreamingPCM
					var ok bool
					if locked {
						entry, ok = lockedPCMCacheGet(cache, key)
					} else {
						entry, ok = cache.get(key)
					}
					if !ok || entry.size != size || entry.chunks[0][size-1] != 1 {
						b.Error("invalid cache hit")
						return
					}
				}
			})
		})
	}
}
