package speechio

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecognizedTextUsesOnlyTopAlternatives(t *testing.T) {
	t.Parallel()

	response := &speechpb.RecognizeResponse{
		Results: []*speechpb.SpeechRecognitionResult{
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: "今日は研究の話をしたい", Confidence: 0.8},
					{Transcript: "採用してはいけない", Confidence: 0.99},
				},
			},
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: "仮説がまだ曖昧です", Confidence: 0.6},
				},
			},
		},
	}

	text, confidence := recognizedText(response)
	if text != "今日は研究の話をしたい 仮説がまだ曖昧です" {
		t.Fatalf("text = %q", text)
	}
	if confidence != 0.6 {
		t.Fatalf("confidence = %f; want conservative minimum 0.60", confidence)
	}
}

func TestRecognizedTextDoesNotAverageAwayALowConfidenceFragment(t *testing.T) {
	t.Parallel()

	response := &speechpb.RecognizeResponse{
		Results: []*speechpb.SpeechRecognitionResult{
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: "前半", Confidence: 0.99},
				},
			},
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: "後半", Confidence: 0.31},
				},
			},
		},
	}

	text, confidence := recognizedText(response)
	if text != "前半 後半" {
		t.Fatalf("text = %q", text)
	}
	if confidence != 0.31 {
		t.Fatalf("confidence = %f; want conservative minimum 0.31", confidence)
	}
}

func TestRecognizedTextKeepsZeroWhenConfidenceIsUnavailable(t *testing.T) {
	t.Parallel()

	response := recognizedResponse("confidenceは未提供です", 0)
	text, confidence := recognizedText(response)
	if text != "confidenceは未提供です" || confidence != 0 {
		t.Fatalf("got (%q, %f); want transcript with unavailable confidence", text, confidence)
	}
}

func TestRecognizedTextHandlesEmptyResponse(t *testing.T) {
	t.Parallel()

	text, confidence := recognizedText(nil)
	if text != "" || confidence != 0 {
		t.Fatalf("got (%q, %f); want empty", text, confidence)
	}
}

func TestTranscribeUsesOnlyConfiguredLongModel(t *testing.T) {
	t.Parallel()

	var calls int
	service := &CloudService{
		speechModel: "long",
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			if request.Config.Model != "long" {
				t.Fatalf("model = %q; want long", request.Config.Model)
			}
			if len(request.Config.LanguageCodes) != 1 ||
				request.Config.LanguageCodes[0] != "ja-JP" {
				t.Fatalf("language codes = %v; want [ja-JP]", request.Config.LanguageCodes)
			}
			if request.Config.GetAutoDecodingConfig() == nil {
				t.Fatal("auto decoding is not configured")
			}
			if request.Config.Features == nil ||
				!request.Config.Features.EnableAutomaticPunctuation {
				t.Fatal("automatic punctuation is not enabled")
			}
			return recognizedResponse("研究テーマについて相談したいです", 0.98), nil
		},
	}

	for turn := 0; turn < 2; turn++ {
		transcript, confidence, err := service.Transcribe(
			context.Background(),
			[]byte("bounded synthetic audio"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if transcript != "研究テーマについて相談したいです" || confidence != 0.98 {
			t.Fatalf("turn %d = (%q, %f)", turn, transcript, confidence)
		}
	}
	if calls != 2 {
		t.Fatalf("recognition calls = %d; want 2", calls)
	}
}

func TestTranscribeReturnsProviderErrorWithoutRetry(t *testing.T) {
	t.Parallel()

	var calls int
	service := &CloudService{
		speechModel: "long",
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			if request.Config.Model != "long" {
				t.Fatalf("model = %q; want long", request.Config.Model)
			}
			return nil, status.Error(
				codes.PermissionDenied,
				"regional model is unavailable",
			)
		},
	}

	_, _, err := service.Transcribe(
		context.Background(),
		[]byte("bounded synthetic audio"),
	)
	if err == nil ||
		!strings.Contains(err.Error(), "regional speech recognition failed") {
		t.Fatalf("error = %v; want wrapped provider error", err)
	}
	if calls != 1 {
		t.Fatalf("recognition calls = %d; want 1", calls)
	}
}

func TestTranscribeRejectsEmptyAudioBeforeProviderCall(t *testing.T) {
	t.Parallel()

	var calls int
	service := &CloudService{
		speechModel: "long",
		recognizeCall: func(
			_ context.Context,
			_ *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			return recognizedResponse("呼ばれてはいけない", 1), nil
		},
	}

	_, _, err := service.Transcribe(context.Background(), nil)
	if !errors.Is(err, ErrNoSpeech) {
		t.Fatalf("error = %v; want ErrNoSpeech", err)
	}
	if calls != 0 {
		t.Fatalf("recognition calls = %d; want 0", calls)
	}
}

func recognizedResponse(
	transcript string,
	confidence float32,
) *speechpb.RecognizeResponse {
	return &speechpb.RecognizeResponse{
		Results: []*speechpb.SpeechRecognitionResult{
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: transcript, Confidence: confidence},
				},
			},
		},
	}
}
