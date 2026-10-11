package speechio

import (
	"context"
	"errors"
	"io"
	"testing"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

type cancelEOFSynthesisStream struct {
	fakeStreamingSynthesizeClient
	cancel context.CancelFunc
}

func (s *cancelEOFSynthesisStream) Recv() (*texttospeechpb.StreamingSynthesizeResponse, error) {
	r, err := s.fakeStreamingSynthesizeClient.Recv()
	if errors.Is(err, io.EOF) {
		s.cancel()
	}
	return r, err
}

func TestSynthesisTerminalCancellation(t *testing.T) {
	for _, route := range []string{"stream", "prepared", "direct", "cached", "prepared cached"} {
		t.Run(route, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &cancelEOFSynthesisStream{cancel: cancel, fakeStreamingSynthesizeClient: fakeStreamingSynthesizeClient{recvResults: []streamingReceiveResult{
				{response: &texttospeechpb.StreamingSynthesizeResponse{AudioContent: []byte{80, 0}}}, {err: io.EOF},
			}}}
			service := cloudServiceWithStream(stream)
			service.streamingPCMCache = newStreamingPCMCache(2, 1024)
			key := newStreamingPCMCacheKey(service.voiceName, "reply")
			cached := route == "cached" || route == "prepared cached"
			if cached {
				service.streamingPCMCache.put(key, cachedStreamingPCM{chunks: [][]byte{{80, 0}}, size: 2})
			}
			if route == "direct" {
				service.synthesizePCMCall = func(context.Context, *texttospeechpb.SynthesizeSpeechRequest) (*texttospeechpb.SynthesizeSpeechResponse, error) {
					return &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte{80, 0}}, nil
				}
			}
			calls := 0
			onChunk := func([]byte) error {
				calls++
				if cached || route == "direct" {
					cancel()
				}
				return nil
			}
			var mime string
			var err error
			if route == "prepared" || route == "prepared cached" {
				prepared, prepareErr := service.PrepareStreamingSynthesis(ctx)
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				defer prepared.Close()
				mime, err = prepared.StreamSynthesize("reply", onChunk)
			} else {
				mime, err = service.StreamSynthesize(ctx, "reply", onChunk)
			}
			if !errors.Is(err, context.Canceled) || mime != "" || calls != 1 {
				t.Fatalf("mime=%q err=%v calls=%d", mime, err, calls)
			}
			_, retained := service.streamingPCMCache.get(key)
			if retained != cached {
				t.Fatalf("canceled synthesis changed completed-cache residency: %t", retained)
			}
		})
	}
}
