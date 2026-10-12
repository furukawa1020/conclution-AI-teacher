package speechio

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

type setupCancelSynthesis struct {
	fakeStreamingSynthesizeClient
	afterSend func()
	sends     int
}

func (s *setupCancelSynthesis) Send(r *texttospeechpb.StreamingSynthesizeRequest) error {
	s.sends++
	err := s.fakeStreamingSynthesizeClient.Send(r)
	s.afterSend()
	return err
}

type setupCancelRecognition struct {
	fakeStreamingRecognizeClient
	afterSend func()
	sends     int
}

func (s *setupCancelRecognition) Send(r *speechpb.StreamingRecognizeRequest) error {
	s.sends++
	err := s.fakeStreamingRecognizeClient.Send(r)
	s.afterSend()
	return err
}

func TestSpeechSetupCancellationBoundaries(t *testing.T) {
	for _, phase := range []string{"open", "configuration"} {
		for _, route := range []string{"synthesis", "recognition"} {
			t.Run(route+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hook := func() {
					if phase == "configuration" {
						cancel()
					}
				}
				var err error
				var sends int
				if route == "synthesis" {
					stream := &setupCancelSynthesis{afterSend: hook}
					service := cloudServiceWithStream(stream)
					service.streamSynthesizeCall = func(context.Context) (streamingSynthesizeClient, error) {
						if phase == "open" {
							cancel()
						}
						return stream, nil
					}
					prepared, callErr := service.PrepareStreamingSynthesis(ctx)
					err, sends = callErr, stream.sends
					if prepared != nil {
						prepared.Close()
						t.Error("canceled capability escaped")
					}
				} else {
					stream := &setupCancelRecognition{afterSend: hook}
					service := streamingTestService(stream, "short")
					service.streamRecognizeCall = func(context.Context) (streamingRecognizeClient, error) {
						if phase == "open" {
							cancel()
						}
						return stream, nil
					}
					session, callErr := service.OpenStreamingTranscription(ctx)
					err, sends = callErr, stream.sends
					if session != nil {
						t.Error("canceled recognition session escaped")
					}
				}
				wantSends := 0
				if phase == "configuration" {
					wantSends = 1
				}
				if !errors.Is(err, context.Canceled) || sends != wantSends {
					t.Fatalf("error=%v sends=%d", err, sends)
				}
			})
		}
	}
}
