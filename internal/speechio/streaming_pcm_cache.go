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
	protected  map[streamingPCMCacheKey]struct{}
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

// protect reserves only the finite server-authored key set. Reservation is
// atomic and does not expand either PCM capacity or the maximum protected set.
func (cache *streamingPCMCache) protect(keys []streamingPCMCacheKey) bool {
	if cache == nil {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	limit := min(cache.maxEntries, defaultStreamingPCMCacheEntries)
	if limit <= 0 || cache.maxBytes <= 0 {
		return false
	}
	pending := make(map[streamingPCMCacheKey]struct{})
	for _, key := range keys {
		if _, exists := cache.protected[key]; exists {
			continue
		}
		if _, exists := pending[key]; exists {
			continue
		}
		if len(cache.protected)+len(pending) >= limit {
			return false
		}
		pending[key] = struct{}{}
	}
	if cache.protected == nil {
		cache.protected = make(map[streamingPCMCacheKey]struct{}, len(pending))
	}
	for key := range pending {
		cache.protected[key] = struct{}{}
	}
	return true
}

// retainsProtected checks residency without touching LRU order or copying PCM.
func (cache *streamingPCMCache) retainsProtected(key streamingPCMCacheKey) bool {
	if cache == nil {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	_, protected := cache.protected[key]
	_, present := cache.entries[key]
	return protected && present
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
	if entry.size <= 0 || entry.size > cache.maxBytes ||
		entry.size > maxStreamingPCMCacheEntryBytes || len(entry.chunks) == 0 {
		return
	}
	actualBytes := 0
	for _, chunk := range entry.chunks {
		if len(chunk) > entry.size-actualBytes {
			return
		}
		actualBytes += len(chunk)
	}
	if actualBytes != entry.size {
		return
	}
	entry = cloneCachedStreamingPCM(entry)

	cache.mu.Lock()
	defer cache.mu.Unlock()

	previous, replacing := cache.entries[key]
	plannedBytes := cache.totalBytes - previous.size + entry.size
	plannedEntries := len(cache.entries)
	if !replacing {
		plannedEntries++
	}
	var evictions []streamingPCMCacheKey
	for _, candidate := range cache.order {
		if plannedEntries <= cache.maxEntries && plannedBytes <= cache.maxBytes {
			break
		}
		if candidate == key {
			continue
		}
		if _, protected := cache.protected[candidate]; protected {
			continue
		}
		evictions = append(evictions, candidate)
		plannedEntries--
		plannedBytes -= cache.entries[candidate].size
	}
	// Reject atomically if protected assets leave insufficient room. Neither
	// a valid previous value nor any planned LRU victim may be lost on failure.
	if plannedEntries > cache.maxEntries || plannedBytes > cache.maxBytes {
		return
	}
	for _, victim := range evictions {
		cache.totalBytes -= cache.entries[victim].size
		delete(cache.entries, victim)
		cache.removeFromOrder(victim)
	}
	if replacing {
		cache.totalBytes -= previous.size
		cache.removeFromOrder(key)
	}
	cache.entries[key] = entry
	cache.order = append(cache.order, key)
	cache.totalBytes += entry.size
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
