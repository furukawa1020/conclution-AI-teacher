package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

func TestStreamSynthesizeReusesOnlyCompletedPCM(t *testing.T) {
	t.Parallel()

	providerCalls := 0
	service := &CloudService{
		voiceName:         "ja-JP-Chirp3-HD-Kore",
		streamingPCMCache: newStreamingPCMCache(4, 1024),
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			providerCalls++
			return &fakeStreamingSynthesizeClient{
				recvResults: []streamingReceiveResult{
					{response: &texttospeechpb.StreamingSynthesizeResponse{
						AudioContent: []byte{1, 2, 3},
					}},
					{response: &texttospeechpb.StreamingSynthesizeResponse{
						AudioContent: []byte{4, 5},
					}},
					{err: io.EOF},
				},
			}, nil
		},
	}

	var first []byte
	if _, err := service.StreamSynthesize(
		context.Background(),
		"same audited reply",
		func(chunk []byte) error {
			first = append(first, chunk...)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	var second []byte
	if _, err := service.StreamSynthesize(
		context.Background(),
		"same audited reply",
		func(chunk []byte) error {
			second = append(second, chunk...)
			chunk[0] = 99
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	if providerCalls != 1 {
		t.Fatalf("provider calls = %d; want 1", providerCalls)
	}
	if !bytes.Equal(first, []byte{1, 2, 3, 4, 5}) {
		t.Fatalf("first audio = %v", first)
	}
	if !bytes.Equal(second, first) {
		t.Fatalf("cached audio = %v; want %v", second, first)
	}

	var third []byte
	if _, err := service.StreamSynthesize(
		context.Background(),
		"same audited reply",
		func(chunk []byte) error {
			third = append(third, chunk...)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(third, first) {
		t.Fatalf("cached audio mutated through callback: %v", third)
	}
}

func TestStreamSynthesizeDoesNotCacheInterruptedAudio(t *testing.T) {
	t.Parallel()

	providerCalls := 0
	service := &CloudService{
		voiceName:         "ja-JP-Chirp3-HD-Kore",
		streamingPCMCache: newStreamingPCMCache(4, 1024),
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			providerCalls++
			return &fakeStreamingSynthesizeClient{
				recvResults: []streamingReceiveResult{
					{response: &texttospeechpb.StreamingSynthesizeResponse{
						AudioContent: []byte{1, 2, 3},
					}},
					{err: io.EOF},
				},
			}, nil
		},
	}

	callbackError := errors.New("playback stopped")
	if _, err := service.StreamSynthesize(
		context.Background(),
		"retry this reply",
		func([]byte) error { return callbackError },
	); !errors.Is(err, callbackError) {
		t.Fatalf("first error = %v; want callback error", err)
	}

	if _, err := service.StreamSynthesize(
		context.Background(),
		"retry this reply",
		func([]byte) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls = %d; interrupted audio was cached", providerCalls)
	}
}

func TestStreamingPCMCacheIsBoundedAndCopiesAudio(t *testing.T) {
	t.Parallel()

	cache := newStreamingPCMCache(2, 5)
	original := []byte{1, 2}
	one := newStreamingPCMCacheKey("voice", "one")
	two := newStreamingPCMCacheKey("voice", "two")
	three := newStreamingPCMCacheKey("voice", "three")
	cache.put(one, cachedStreamingPCM{chunks: [][]byte{original}, size: 2})
	cache.put(two, cachedStreamingPCM{chunks: [][]byte{{3, 4}}, size: 2})
	cache.put(three, cachedStreamingPCM{chunks: [][]byte{{5, 6}}, size: 2})
	original[0] = 99

	if _, ok := cache.get(one); ok {
		t.Fatal("oldest entry survived entry and byte bounds")
	}
	entry, ok := cache.get(two)
	if !ok || !bytes.Equal(entry.chunks[0], []byte{3, 4}) {
		t.Fatalf("second entry = %+v, %v", entry, ok)
	}
	entry.chunks[0][0] = 88
	again, ok := cache.get(two)
	if !ok || !bytes.Equal(again.chunks[0], []byte{3, 4}) {
		t.Fatalf("cache storage mutated through returned bytes: %+v", again)
	}
}

func TestStreamingPCMCollectorRejectsOversizeAudio(t *testing.T) {
	t.Parallel()

	collector := newStreamingPCMCollector(4)
	collector.add([]byte{1, 2, 3})
	collector.add([]byte{4, 5})
	if _, ok := collector.complete(); ok {
		t.Fatal("oversize partial audio became cacheable")
	}
}
