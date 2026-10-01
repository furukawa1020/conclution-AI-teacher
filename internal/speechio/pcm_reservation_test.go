package speechio

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPCMReservationIsAtomicAndBounded(t *testing.T) {
	keys := make([]streamingPCMCacheKey, defaultStreamingPCMCacheEntries+1)
	for index := range keys {
		keys[index] = newStreamingPCMCacheKey("voice", fmt.Sprint(index))
	}
	cache := newStreamingPCMCache(defaultStreamingPCMCacheEntries+100, 1024)
	if !cache.protect(keys[:defaultStreamingPCMCacheEntries]) {
		t.Fatal("bounded set rejected")
	}
	if !cache.protect(keys[:defaultStreamingPCMCacheEntries]) {
		t.Fatal("duplicate reservations consumed capacity")
	}
	if cache.protect(keys) || len(cache.protected) != defaultStreamingPCMCacheEntries {
		t.Fatal("protection exceeded finite-set bound")
	}
	if _, ok := cache.protected[keys[len(keys)-1]]; ok {
		t.Fatal("failed reservation partially mutated protection")
	}
	for _, limits := range [][2]int{{0, 1}, {-1, 1}, {1, 0}, {1, -1}} {
		invalid := newStreamingPCMCache(limits[0], limits[1])
		if invalid.protect(keys[:1]) {
			t.Fatalf("invalid capacity accepted: %v", limits)
		}
		invalid.put(keys[0], cachedStreamingPCM{chunks: [][]byte{{1}}, size: 1})
		if len(invalid.entries) != 0 {
			t.Fatalf("invalid capacity stored PCM: %v", limits)
		}
	}
	var missing *streamingPCMCache
	if missing.protect(keys[:1]) || missing.retainsProtected(keys[0]) {
		t.Fatal("nil cache advertised residency")
	}
}

func TestPCMParallelReservationCannotExceedBound(t *testing.T) {
	cache := newStreamingPCMCache(defaultStreamingPCMCacheEntries, 1024)
	var workers sync.WaitGroup
	var admitted atomic.Int32
	for index := range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if cache.protect([]streamingPCMCacheKey{newStreamingPCMCacheKey("voice", fmt.Sprint(index))}) {
				admitted.Add(1)
			}
		}()
	}
	workers.Wait()
	if admitted.Load() != defaultStreamingPCMCacheEntries || len(cache.protected) != defaultStreamingPCMCacheEntries {
		t.Fatalf("admitted=%d protected=%d", admitted.Load(), len(cache.protected))
	}
}

func TestPCMRejectedAdmissionPreservesProtectedAndPreviousValues(t *testing.T) {
	cache := newStreamingPCMCache(3, 10)
	one, two := newStreamingPCMCacheKey("voice", "fixed1"), newStreamingPCMCacheKey("voice", "fixed2")
	dynamic := newStreamingPCMCacheKey("voice", "dynamic")
	newKey := newStreamingPCMCacheKey("voice", "new")
	if !cache.protect([]streamingPCMCacheKey{one, two}) {
		t.Fatal("reservation failed")
	}
	cache.put(one, cachedStreamingPCM{chunks: [][]byte{{1, 0, 1, 0}}, size: 4})
	cache.put(two, cachedStreamingPCM{chunks: [][]byte{{2, 0, 2, 0}}, size: 4})
	cache.put(dynamic, cachedStreamingPCM{chunks: [][]byte{{3, 0}}, size: 2})
	beforeOrder := append([]streamingPCMCacheKey(nil), cache.order...)
	for _, attempted := range []struct {
		key   streamingPCMCacheKey
		value cachedStreamingPCM
	}{
		{one, cachedStreamingPCM{chunks: [][]byte{make([]byte, 8)}, size: 8}},
		{dynamic, cachedStreamingPCM{chunks: [][]byte{make([]byte, 4)}, size: 4}},
		{newKey, cachedStreamingPCM{chunks: [][]byte{make([]byte, 8)}, size: 8}},
		{one, cachedStreamingPCM{chunks: [][]byte{make([]byte, 20)}, size: 1}},
	} {
		cache.put(attempted.key, attempted.value)
		if cache.totalBytes != 10 || len(cache.entries) != 3 || !reflect.DeepEqual(cache.order, beforeOrder) {
			t.Fatal("failed admission changed size, entries, or LRU")
		}
		if !bytes.Equal(cache.entries[one].chunks[0], []byte{1, 0, 1, 0}) || !bytes.Equal(cache.entries[dynamic].chunks[0], []byte{3, 0}) {
			t.Fatal("failed admission lost previous PCM")
		}
	}
	cache.put(one, cachedStreamingPCM{chunks: [][]byte{{4, 0}}, size: 2})
	cache.put(newKey, cachedStreamingPCM{chunks: [][]byte{{5, 0, 5, 0}}, size: 4})
	if _, ok := cache.entries[dynamic]; ok {
		t.Fatal("unprotected LRU was not evicted")
	}
	if !cache.retainsProtected(one) || !cache.retainsProtected(two) || cache.totalBytes != 10 || len(cache.entries) != 3 {
		t.Fatal("valid replacement broke reservation or capacity")
	}
}

func TestPCMProtectedEntriesKeepDeepCopyIsolation(t *testing.T) {
	cache := newStreamingPCMCache(1, 4)
	key := newStreamingPCMCacheKey("voice", "fixed")
	if !cache.protect([]streamingPCMCacheKey{key}) {
		t.Fatal("reservation failed")
	}
	input := []byte{40, 0}
	cache.put(key, cachedStreamingPCM{chunks: [][]byte{input}, size: 2})
	input[0] = 99
	first, ok := cache.get(key)
	if !ok || first.chunks[0][0] != 40 {
		t.Fatal("input mutated protected PCM")
	}
	first.chunks[0][0] = 88
	second, ok := cache.get(key)
	if !ok || second.chunks[0][0] != 40 {
		t.Fatal("consumer mutated protected PCM")
	}
	cache.put(newStreamingPCMCacheKey("voice", "dynamic"), cachedStreamingPCM{chunks: [][]byte{{5, 0}}, size: 2})
	if !cache.retainsProtected(key) || len(cache.entries) != 1 {
		t.Fatal("dynamic entry displaced fully reserved capacity")
	}
}
