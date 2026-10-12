package speechio

import (
	"context"
	"testing"
)

func TestCanceledWarmupDoesNotReserveCapacity(t *testing.T) {
	service := cloudServiceWithStream(&fakeStreamingSynthesizeClient{})
	cache := newStreamingPCMCache(2, 1024)
	service.streamingPCMCache = cache
	old := newStreamingPCMCacheKey(service.voiceName, "existing")
	cache.protect([]streamingPCMCacheKey{old})
	cache.put(old, cachedStreamingPCM{chunks: [][]byte{{80, 0}}, size: 2})
	service.warmSynthesisKeys = map[streamingPCMCacheKey]struct{}{old: {}}
	calls := 0
	service.streamSynthesizeCall = func(context.Context) (streamingSynthesizeClient, error) {
		calls++
		return &fakeStreamingSynthesizeClient{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := service.WarmStreamingSynthesis(ctx, []string{"new", " new ", ""}, 4)
	if result.Requested != 1 || result.Warmed != 0 || result.Failed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if calls != 0 || len(cache.protected) != 1 || len(service.warmSynthesisKeys) != 1 || !cache.retainsProtected(old) {
		t.Fatal("canceled warmup changed reservations, residency or provider calls")
	}
	if !cache.protect([]streamingPCMCacheKey{newStreamingPCMCacheKey(service.voiceName, "later")}) {
		t.Fatal("canceled warmup consumed future capacity")
	}
}
