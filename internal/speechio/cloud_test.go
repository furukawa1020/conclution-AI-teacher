package speechio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
					{Transcript: "採用してはいけない候補", Confidence: 0.99},
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
	if confidence < 0.69 || confidence > 0.71 {
		t.Fatalf("confidence = %f; want about 0.70", confidence)
	}
}

func TestRecognizedTextHandlesEmptyResponse(t *testing.T) {
	t.Parallel()

	text, confidence := recognizedText(nil)
	if text != "" || confidence != 0 {
		t.Fatalf("got (%q, %f); want empty", text, confidence)
	}
}

func TestPrimaryModelUnavailableOnlyMatchesModelAvailabilityDenials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "live control plane denial",
			err: status.Error(
				codes.PermissionDenied,
				"Permission denied for project 123 on model chirp_3 locale ja-JP. It is no longer generally available.",
			),
			want: true,
		},
		{
			name: "wrapped denial fails closed",
			err: fmt.Errorf(
				"recognize: %w",
				status.Error(
					codes.PermissionDenied,
					"Permission denied for project 123 on model chirp_3 locale ja-JP. It is no longer generally available.",
				),
			),
		},
		{
			name: "generic IAM denial must fail closed",
			err: status.Error(
				codes.PermissionDenied,
				"Permission speech.recognizers.recognize denied.",
			),
		},
		{
			name: "different model denial",
			err: status.Error(
				codes.PermissionDenied,
				"Permission denied for project 123 on model chirp_2 locale ja-JP. It is no longer generally available.",
			),
		},
		{
			name: "different locale denial",
			err: status.Error(
				codes.PermissionDenied,
				"Permission denied for project 123 on model chirp_3 locale en-US. It is no longer generally available.",
			),
		},
		{
			name: "invalid audio does not downgrade",
			err:  status.Error(codes.InvalidArgument, "Audio decoding failed."),
		},
		{
			name: "ordinary error",
			err:  errors.New("network failure"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := primaryModelUnavailable(test.err, "chirp_3"); got != test.want {
				t.Fatalf("primaryModelUnavailable() = %v; want %v", got, test.want)
			}
		})
	}
}

func TestTranscribeFallsBackOnceAndCachesTheRegionalModel(t *testing.T) {
	t.Parallel()

	var models []string
	service := &CloudService{
		speechModel:     "chirp_3",
		fallbackModel:   "short",
		fallbackEnabled: true,
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			models = append(models, request.Config.Model)
			if request.Config.Model == "chirp_3" {
				return nil, status.Error(
					codes.PermissionDenied,
					"Permission denied for project 123 on model chirp_3 locale ja-JP. It is no longer generally available.",
				)
			}
			return recognizedResponse("日本の首都はどこですか", 0.98), nil
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
		if transcript != "日本の首都はどこですか" || confidence != 0.98 {
			t.Fatalf("turn %d = (%q, %f)", turn, transcript, confidence)
		}
	}
	wantModels := []string{"chirp_3", "short", "short"}
	if fmt.Sprint(models) != fmt.Sprint(wantModels) {
		t.Fatalf("models = %v; want %v", models, wantModels)
	}
}

func TestTranscribeDoesNotDowngradeGenericPermissionDenials(t *testing.T) {
	t.Parallel()

	var calls int
	service := &CloudService{
		speechModel:     "chirp_3",
		fallbackModel:   "short",
		fallbackEnabled: true,
		recognizeCall: func(
			_ context.Context,
			_ *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			return nil, status.Error(
				codes.PermissionDenied,
				"Permission speech.recognizers.recognize denied.",
			)
		},
	}

	if _, _, err := service.Transcribe(
		context.Background(),
		[]byte("bounded synthetic audio"),
	); err == nil {
		t.Fatal("Transcribe succeeded; want permission failure")
	}
	if calls != 1 || service.fallbackActive.Load() {
		t.Fatalf("calls = %d, fallback active = %v", calls, service.fallbackActive.Load())
	}
}

func TestTranscribeDoesNotRetryAfterContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	service := &CloudService{
		speechModel:     "chirp_3",
		fallbackModel:   "short",
		fallbackEnabled: true,
		recognizeCall: func(
			_ context.Context,
			_ *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			return nil, status.Error(
				codes.PermissionDenied,
				"Permission denied for project 123 on model chirp_3 locale ja-JP. It is no longer generally available.",
			)
		},
	}

	if _, _, err := service.Transcribe(
		ctx,
		[]byte("bounded synthetic audio"),
	); err == nil {
		t.Fatal("Transcribe succeeded after cancellation")
	}
	if calls != 1 || service.fallbackActive.Load() {
		t.Fatalf("calls = %d, fallback active = %v", calls, service.fallbackActive.Load())
	}
}

func TestFallbackFailureDoesNotRetryIndefinitely(t *testing.T) {
	t.Parallel()

	var models []string
	service := &CloudService{
		speechModel:     "chirp_3",
		fallbackModel:   "short",
		fallbackEnabled: true,
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			models = append(models, request.Config.Model)
			if request.Config.Model == "chirp_3" {
				return nil, status.Error(
					codes.PermissionDenied,
					"Permission denied for project 123 on model chirp_3 locale ja-JP. It is no longer generally available.",
				)
			}
			return nil, status.Error(codes.Unavailable, "regional service unavailable")
		},
	}

	for range 2 {
		if _, _, err := service.Transcribe(
			context.Background(),
			[]byte("bounded synthetic audio"),
		); err == nil {
			t.Fatal("Transcribe succeeded; want fallback failure")
		}
	}
	wantModels := []string{"chirp_3", "short", "short"}
	if fmt.Sprint(models) != fmt.Sprint(wantModels) {
		t.Fatalf("models = %v; want %v", models, wantModels)
	}
}

func TestGuardedFallbackIsLimitedToReviewedTokyoModelPair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		location string
		primary  string
		fallback string
		want     bool
	}{
		{
			location: "asia-northeast1",
			primary:  "chirp_3",
			fallback: "short",
			want:     true,
		},
		{location: "us", primary: "chirp_3", fallback: "short"},
		{location: "asia-northeast1", primary: "chirp_2", fallback: "short"},
		{location: "asia-northeast1", primary: "chirp_3", fallback: "long"},
	}
	for _, test := range tests {
		if got := guardedFallbackEnabled(
			test.location,
			test.primary,
			test.fallback,
		); got != test.want {
			t.Fatalf(
				"guardedFallbackEnabled(%q, %q, %q) = %v; want %v",
				test.location,
				test.primary,
				test.fallback,
				got,
				test.want,
			)
		}
	}
}

func TestConcurrentFallbackSelectionIsRaceSafe(t *testing.T) {
	t.Parallel()

	var primaryCalls atomic.Int32
	var fallbackCalls atomic.Int32
	service := &CloudService{
		speechModel:     "chirp_3",
		fallbackModel:   "short",
		fallbackEnabled: true,
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			if request.Config.Model == "chirp_3" {
				primaryCalls.Add(1)
				return nil, status.Error(
					codes.PermissionDenied,
					"Permission denied for project 123 on model chirp_3 locale ja-JP. It is no longer generally available.",
				)
			}
			fallbackCalls.Add(1)
			return recognizedResponse("聞き取れました", 0.95), nil
		},
	}

	const turns = 50
	var wait sync.WaitGroup
	errs := make(chan error, turns)
	for range turns {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := service.Transcribe(
				context.Background(),
				[]byte("bounded synthetic audio"),
			)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if primaryCalls.Load() < 1 || primaryCalls.Load() > turns {
		t.Fatalf("primary calls = %d", primaryCalls.Load())
	}
	if fallbackCalls.Load() != turns {
		t.Fatalf("fallback calls = %d; want %d", fallbackCalls.Load(), turns)
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
