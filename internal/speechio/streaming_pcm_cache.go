package speechio

import (
	"crypto/sha256"
	"strings"
	"sync"
)

const (
	defaultStreamingPCMCacheEntries = 24
	defaultStreamingPCMCacheBytes   = 8 << 20
	maxStreamingPCMCacheEntryBytes  = 1 << 20
)

type cachedStreamingPCM struct {
	chunks [][]byte
	size   int
}

type streamingPCMCacheKey [sha256.Size]byte

type streamingPCMCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	totalBytes int
	order      []streamingPCMCacheKey
	entries    map[streamingPCMCacheKey]cachedStreamingPCM
}

func newStreamingPCMCache(maxEntries int, maxBytes int) *streamingPCMCache {
	return &streamingPCMCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entries:    make(map[streamingPCMCacheKey]cachedStreamingPCM),
	}
}

func newStreamingPCMCacheKey(voiceName string, text string) streamingPCMCacheKey {
	return sha256.Sum256(
		[]byte(strings.TrimSpace(voiceName) + "\x00" + strings.TrimSpace(text)),
	)
}

func (cache *streamingPCMCache) get(key streamingPCMCacheKey) (cachedStreamingPCM, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	entry, ok := cache.entries[key]
	if !ok {
		return cachedStreamingPCM{}, false
	}
	// A successful delivery is real use, not a passive lookup. Move it to the
	// newest position so frequently spoken audited cues survive unrelated
	// one-off replies instead of being evicted in insertion (FIFO) order.
	cache.removeFromOrder(key)
	cache.order = append(cache.order, key)
	return cloneCachedStreamingPCM(entry), true
}

func (cache *streamingPCMCache) put(key streamingPCMCacheKey, entry cachedStreamingPCM) {
	if entry.size <= 0 || entry.size > cache.maxBytes || len(entry.chunks) == 0 {
		return
	}
	entry = cloneCachedStreamingPCM(entry)

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if previous, ok := cache.entries[key]; ok {
		cache.totalBytes -= previous.size
		cache.removeFromOrder(key)
	}
	cache.entries[key] = entry
	cache.order = append(cache.order, key)
	cache.totalBytes += entry.size

	for len(cache.order) > cache.maxEntries || cache.totalBytes > cache.maxBytes {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		if removed, ok := cache.entries[oldest]; ok {
			cache.totalBytes -= removed.size
			delete(cache.entries, oldest)
		}
	}
}

func (cache *streamingPCMCache) removeFromOrder(key streamingPCMCacheKey) {
	for index, candidate := range cache.order {
		if candidate == key {
			cache.order = append(cache.order[:index], cache.order[index+1:]...)
			return
		}
	}
}

func cloneCachedStreamingPCM(entry cachedStreamingPCM) cachedStreamingPCM {
	cloned := cachedStreamingPCM{
		size:   entry.size,
		chunks: make([][]byte, len(entry.chunks)),
	}
	for index, chunk := range entry.chunks {
		cloned.chunks[index] = append([]byte(nil), chunk...)
	}
	return cloned
}

type streamingPCMCollector struct {
	maxBytes       int
	size           int
	completeEnough bool
	chunks         [][]byte
}

func newStreamingPCMCollector(maxBytes int) *streamingPCMCollector {
	return &streamingPCMCollector{
		maxBytes:       maxBytes,
		completeEnough: true,
	}
}

func (collector *streamingPCMCollector) add(chunk []byte) {
	if !collector.completeEnough || len(chunk) > collector.maxBytes-collector.size {
		collector.completeEnough = false
		collector.chunks = nil
		collector.size = 0
		return
	}
	collector.chunks = append(collector.chunks, append([]byte(nil), chunk...))
	collector.size += len(chunk)
}

func (collector *streamingPCMCollector) complete() (cachedStreamingPCM, bool) {
	if !collector.completeEnough || collector.size == 0 || len(collector.chunks) == 0 {
		return cachedStreamingPCM{}, false
	}
	return cachedStreamingPCM{
		chunks: collector.chunks,
		size:   collector.size,
	}, true
}
