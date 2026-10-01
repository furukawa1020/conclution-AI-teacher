package speechio

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

func residencyTestService(cache *streamingPCMCache, pcm []byte) (*CloudService, *atomic.Int32) {
	var calls atomic.Int32
	return &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore", streamingPCMCache: cache,
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			return nil, errors.New("stream unavailable")
		},
		synthesizePCMCall: func(context.Context, *texttospeechpb.SynthesizeSpeechRequest) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			calls.Add(1)
			return &texttospeechpb.SynthesizeSpeechResponse{AudioContent: pcm}, nil
		},
	}, &calls
}

func TestWarmFixedPCMRemainsResidentAfterGeneratedChurn(t *testing.T) {
	cache := newStreamingPCMCache(defaultStreamingPCMCacheEntries, defaultStreamingPCMCacheBytes)
	service, calls := residencyTestService(cache, []byte{40, 0})
	cues := make([]string, 21)
	for index := range cues {
		cues[index] = fmt.Sprintf("fixed cue %02d", index)
	}
	result := service.WarmStreamingSynthesis(context.Background(), cues, 1)
	if result.Requested != len(cues) || result.Warmed != len(cues) || result.Failed != 0 {
		t.Fatalf("warm result=%+v", result)
	}
	for index := range 48 {
		if _, err := service.StreamSynthesize(context.Background(), fmt.Sprintf("generated reply %02d", index), func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	beforeReplay := calls.Load()
	for _, cue := range cues {
		if _, err := service.StreamSynthesize(context.Background(), cue, func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != beforeReplay {
		t.Fatalf("fixed cue replay opened %d additional provider calls", calls.Load()-beforeReplay)
	}
	if len(cache.entries) > defaultStreamingPCMCacheEntries || cache.totalBytes > defaultStreamingPCMCacheBytes {
		t.Fatalf("cache exceeded unchanged limits: entries=%d bytes=%d", len(cache.entries), cache.totalBytes)
	}
	if _, ok := cache.get(newStreamingPCMCacheKey(service.voiceName, "generated reply 00")); ok {
		t.Fatal("old generated reply bypassed LRU eviction")
	}
	if _, ok := cache.get(newStreamingPCMCacheKey(service.voiceName, "generated reply 47")); !ok {
		t.Fatal("new generated reply lost remaining dynamic capacity")
	}
}

func TestWarmCountsSuccessfulFinalResidency(t *testing.T) {
	for _, test := range []struct {
		name       string
		cache      *streamingPCMCache
		pcm        []byte
		wantWarmed int
	}{
		{"missing cache", nil, []byte{40, 0}, 0},
		{"byte capacity", newStreamingPCMCache(2, 2), []byte{40, 0}, 1},
		{"entry capacity", newStreamingPCMCache(1, 32), []byte{40, 0}, 0},
		{"quiet output", newStreamingPCMCache(2, 32), []byte{0, 0}, 0},
		{"empty output", newStreamingPCMCache(2, 32), nil, 0},
		{"oversized output", newStreamingPCMCache(2, defaultStreamingPCMCacheBytes), append([]byte{40, 0}, make([]byte, maxStreamingPCMCacheEntryBytes)...), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := residencyTestService(test.cache, test.pcm)
			result := service.WarmStreamingSynthesis(context.Background(), []string{"first cue", "second cue"}, 1)
			if result.Requested != 2 || result.Warmed != test.wantWarmed || result.Failed != 2-test.wantWarmed {
				t.Fatalf("warm result=%+v", result)
			}
		})
	}
}

func TestWarmStaleEntryCannotHideFailedDelivery(t *testing.T) {
	cache := newStreamingPCMCache(2, 32)
	service, _ := residencyTestService(cache, []byte{40, 0})
	const cue = "stale cue"
	cache.put(newStreamingPCMCacheKey(service.voiceName, cue), cachedStreamingPCM{chunks: [][]byte{{40, 0}}, size: 2})
	service.streamSynthesizeCall = nil
	result := service.WarmStreamingSynthesis(context.Background(), []string{cue}, 1)
	if result.Requested != 1 || result.Warmed != 0 || result.Failed != 1 {
		t.Fatalf("stale entry hid delivery failure: %+v", result)
	}
}

func TestWarmProtectionUnionRejectsGrowthWithoutProviderWork(t *testing.T) {
	cache := newStreamingPCMCache(2, 32)
	service, calls := residencyTestService(cache, []byte{40, 0})
	first := service.WarmStreamingSynthesis(context.Background(), []string{"first", "second"}, 1)
	if first.Warmed != 2 {
		t.Fatalf("first=%+v", first)
	}
	before := calls.Load()
	duplicate := service.WarmStreamingSynthesis(context.Background(), []string{"first", "first", "second"}, 1)
	if duplicate.Requested != 2 || duplicate.Warmed != 2 || calls.Load() != before {
		t.Fatalf("duplicate=%+v calls=%d", duplicate, calls.Load())
	}
	for range 8 {
		rejected := service.WarmStreamingSynthesis(context.Background(), []string{"first", "third"}, 1)
		if rejected.Requested != 2 || rejected.Warmed != 0 || rejected.Failed != 2 {
			t.Fatalf("rejected=%+v", rejected)
		}
	}
	if calls.Load() != before || len(cache.protected) != 2 || len(service.warmSynthesisKeys) != 2 {
		t.Fatal("failed reservation expanded routing state or called provider")
	}
	if !cache.retainsProtected(newStreamingPCMCacheKey(service.voiceName, "first")) || !cache.retainsProtected(newStreamingPCMCacheKey(service.voiceName, "second")) {
		t.Fatal("failed reservation displaced ready assets")
	}
}

func TestWarmCancellationAndProviderFailureCannotAdvertiseReady(t *testing.T) {
	for _, mode := range []string{"canceled", "nil response", "provider failure"} {
		t.Run(mode, func(t *testing.T) {
			cache := newStreamingPCMCache(2, 32)
			service, calls := residencyTestService(cache, []byte{40, 0})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cache.put(newStreamingPCMCacheKey(service.voiceName, "first"), cachedStreamingPCM{chunks: [][]byte{{40, 0}}, size: 2})
				cancel()
			} else {
				service.synthesizePCMCall = func(context.Context, *texttospeechpb.SynthesizeSpeechRequest) (*texttospeechpb.SynthesizeSpeechResponse, error) {
					if mode == "nil response" {
						return nil, nil
					}
					return nil, errors.New("provider unavailable")
				}
			}
			result := service.WarmStreamingSynthesis(ctx, []string{"first", "second"}, 2)
			if result.Requested != 2 || result.Warmed != 0 || result.Failed != 2 {
				t.Fatalf("invalid readiness=%+v", result)
			}
			if mode == "canceled" && calls.Load() != 0 {
				t.Fatal("canceled warm contacted provider")
			}
		})
	}
}
