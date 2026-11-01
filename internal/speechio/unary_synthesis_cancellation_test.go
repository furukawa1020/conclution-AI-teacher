package speechio

import (
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"context"
	"errors"
	"testing"
)

func TestUnarySynthesisCancellationBoundary(t *testing.T) {
	for _, mode := range []string{"before", "during", "error", "success"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pcm := []byte{1, 2, 3, 4}
			calls := 0
			service := &CloudService{synthesizeCall: func(context.Context, *texttospeechpb.SynthesizeSpeechRequest) (*texttospeechpb.SynthesizeSpeechResponse, error) {
				calls++
				if mode == "during" {
					cancel()
				}
				response := &texttospeechpb.SynthesizeSpeechResponse{AudioContent: pcm}
				if mode == "error" {
					return response, errors.New("provider")
				}
				return response, nil
			}}
			if mode == "before" {
				cancel()
			}
			audio, mime, err := service.Synthesize(ctx, "こんにちは")
			if mode == "success" {
				if err != nil || len(audio) != 4 || mime != "audio/mpeg" {
					t.Fatal("success changed")
				}
				return
			}
			if len(audio) != 0 || mime != "" || err == nil {
				t.Fatal("failed synthesis returned audio")
			}
			if mode == "before" {
				if calls != 0 {
					t.Fatal("cancelled synthesis reached provider")
				}
				return
			}
			for _, b := range pcm {
				if b != 0 {
					t.Fatal("discarded audio not cleared")
				}
			}
			if mode == "during" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
