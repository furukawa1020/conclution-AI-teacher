package speechio

import (
	"cloud.google.com/go/speech/apiv2/speechpb"
	"context"
	"errors"
	"testing"
)

func TestRecognitionCancellationBoundary(t *testing.T) {
	for _, before := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		service := &CloudService{speechModel: conversationSpeechModel}
		service.recognizeCall = func(context.Context, *speechpb.RecognizeRequest) (*speechpb.RecognizeResponse, error) {
			calls++
			cancel()
			return &speechpb.RecognizeResponse{Results: []*speechpb.SpeechRecognitionResult{{Alternatives: []*speechpb.SpeechRecognitionAlternative{{Transcript: "こんにちは", Confidence: .9}}}}}, nil
		}
		if before {
			cancel()
		}
		text, _, err := service.Transcribe(ctx, []byte{1, 2})
		cancel()
		if text != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled recognition: %q %v", text, err)
		}
		if before && calls != 0 {
			t.Fatal("cancelled request reached provider")
		}
		if !before && calls != 1 {
			t.Fatal("provider was not exercised")
		}
	}
}
