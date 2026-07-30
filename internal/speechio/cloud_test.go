package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
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

func TestStreamSynthesizeSendsBoundedPCMRequestAndDeliversChunksInOrder(t *testing.T) {
	t.Parallel()

	stream := &fakeStreamingSynthesizeClient{
		recvResults: []streamingReceiveResult{
			{response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: []byte{1, 2, 3},
			}},
			{response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: []byte{4, 5},
			}},
			{err: io.EOF},
		},
	}
	service := &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		streamSynthesizeCall: func(
			context.Context,
		) (streamingSynthesizeClient, error) {
			return stream, nil
		},
	}

	var audio []byte
	contentType, err := service.StreamSynthesize(
		context.Background(),
		"  論点から答えます。  ",
		func(chunk []byte) error {
			audio = append(audio, chunk...)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != StreamingAudioContentType {
		t.Fatalf("content type = %q; want %q", contentType, StreamingAudioContentType)
	}
	if !bytes.Equal(audio, []byte{1, 2, 3, 4, 5}) {
		t.Fatalf("audio = %v", audio)
	}
	if !stream.closeCalled {
		t.Fatal("CloseSend was not called")
	}
	if len(stream.sent) != 2 {
		t.Fatalf("sent requests = %d; want config then input", len(stream.sent))
	}

	config := stream.sent[0].GetStreamingConfig()
	if config == nil || stream.sent[0].GetInput() != nil {
		t.Fatal("first request must contain only streaming config")
	}
	if config.Voice == nil ||
		config.Voice.LanguageCode != "ja-JP" ||
		config.Voice.Name != "ja-JP-Chirp3-HD-Kore" {
		t.Fatalf("voice = %+v", config.Voice)
	}
	if config.StreamingAudioConfig == nil ||
		config.StreamingAudioConfig.AudioEncoding != texttospeechpb.AudioEncoding_PCM ||
		config.StreamingAudioConfig.SampleRateHertz != StreamingSampleRateHertz {
		t.Fatalf("streaming audio config = %+v", config.StreamingAudioConfig)
	}

	input := stream.sent[1].GetInput()
	if input == nil || stream.sent[1].GetStreamingConfig() != nil {
		t.Fatal("second request must contain only streaming input")
	}
	if input.GetText() != "論点から答えます。" {
		t.Fatalf("streaming input = %q", input.GetText())
	}
}

func TestStreamSynthesizeFailsClosedOnTransportErrors(t *testing.T) {
	t.Parallel()

	providerError := errors.New("provider transport failed")
	tests := []struct {
		name   string
		stream *fakeStreamingSynthesizeClient
	}{
		{
			name: "config send",
			stream: &fakeStreamingSynthesizeClient{
				sendErrorAt: 1,
				sendErr:     providerError,
			},
		},
		{
			name: "input send",
			stream: &fakeStreamingSynthesizeClient{
				sendErrorAt: 2,
				sendErr:     providerError,
			},
		},
		{
			name: "close send",
			stream: &fakeStreamingSynthesizeClient{
				closeErr: providerError,
			},
		},
		{
			name: "receive",
			stream: &fakeStreamingSynthesizeClient{
				recvResults: []streamingReceiveResult{{err: providerError}},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			service := cloudServiceWithStream(test.stream)
			deliveries := 0
			_, err := service.StreamSynthesize(
				context.Background(),
				"安全な回答です。",
				func([]byte) error {
					deliveries++
					return nil
				},
			)
			if !errors.Is(err, providerError) {
				t.Fatalf("error = %v; want wrapped provider error", err)
			}
			if deliveries != 0 {
				t.Fatalf("deliveries = %d; want none", deliveries)
			}
		})
	}
}

func TestStreamSynthesizeFailsClosedWhenStreamCannotStart(t *testing.T) {
	t.Parallel()

	startError := errors.New("stream unavailable")
	service := &CloudService{
		streamSynthesizeCall: func(
			context.Context,
		) (streamingSynthesizeClient, error) {
			return nil, startError
		},
	}
	_, err := service.StreamSynthesize(
		context.Background(),
		"安全な回答です。",
		func([]byte) error { return nil },
	)
	if !errors.Is(err, startError) {
		t.Fatalf("start error = %v", err)
	}

	service.streamSynthesizeCall = func(
		context.Context,
	) (streamingSynthesizeClient, error) {
		return nil, nil
	}
	_, err = service.StreamSynthesize(
		context.Background(),
		"安全な回答です。",
		func([]byte) error { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "returned no stream") {
		t.Fatalf("nil stream error = %v", err)
	}
}

func TestStreamSynthesizeRejectsEmptyProviderOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []streamingReceiveResult
		want    error
	}{
		{
			name:    "empty stream",
			results: []streamingReceiveResult{{err: io.EOF}},
			want:    ErrNoStreamingAudio,
		},
		{
			name:    "nil response",
			results: []streamingReceiveResult{{response: nil}},
			want:    ErrEmptyStreamingChunk,
		},
		{
			name:    "empty chunk",
			results: []streamingReceiveResult{{response: &texttospeechpb.StreamingSynthesizeResponse{}}},
			want:    ErrEmptyStreamingChunk,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			service := cloudServiceWithStream(&fakeStreamingSynthesizeClient{
				recvResults: test.results,
			})
			_, err := service.StreamSynthesize(
				context.Background(),
				"安全な回答です。",
				func([]byte) error { return nil },
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestStreamSynthesizeEnforcesChunkAndTotalBounds(t *testing.T) {
	t.Parallel()

	oversizedChunk := make([]byte, maxStreamingAudioChunkSize+1)
	service := cloudServiceWithStream(&fakeStreamingSynthesizeClient{
		recvResults: []streamingReceiveResult{
			{response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: oversizedChunk,
			}},
		},
	})
	_, err := service.StreamSynthesize(
		context.Background(),
		"安全な回答です。",
		func([]byte) error {
			t.Fatal("oversized chunk must not be delivered")
			return nil
		},
	)
	if !errors.Is(err, ErrStreamingChunkTooLarge) {
		t.Fatalf("chunk error = %v", err)
	}

	maxChunk := make([]byte, maxStreamingAudioChunkSize)
	results := make([]streamingReceiveResult, 0, 18)
	for range maxStreamingAudioTotalSize / maxStreamingAudioChunkSize {
		results = append(results, streamingReceiveResult{
			response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: maxChunk,
			},
		})
	}
	results = append(results, streamingReceiveResult{
		response: &texttospeechpb.StreamingSynthesizeResponse{
			AudioContent: []byte{1},
		},
	})
	service = cloudServiceWithStream(&fakeStreamingSynthesizeClient{
		recvResults: results,
	})
	deliveries := 0
	_, err = service.StreamSynthesize(
		context.Background(),
		"安全な回答です。",
		func([]byte) error {
			deliveries++
			return nil
		},
	)
	if !errors.Is(err, ErrStreamingAudioTooLarge) {
		t.Fatalf("total error = %v", err)
	}
	if deliveries != maxStreamingAudioTotalSize/maxStreamingAudioChunkSize {
		t.Fatalf("deliveries = %d; want chunks up to total bound", deliveries)
	}
}

func TestStreamSynthesizePropagatesCallbackAndContextCancellation(t *testing.T) {
	t.Parallel()

	callbackError := errors.New("downstream closed")
	service := cloudServiceWithStream(&fakeStreamingSynthesizeClient{
		recvResults: []streamingReceiveResult{
			{response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: []byte{1},
			}},
		},
	})
	_, err := service.StreamSynthesize(
		context.Background(),
		"安全な回答です。",
		func([]byte) error { return callbackError },
	)
	if !errors.Is(err, callbackError) {
		t.Fatalf("callback error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	service = &CloudService{
		streamSynthesizeCall: func(
			context.Context,
		) (streamingSynthesizeClient, error) {
			called = true
			return &fakeStreamingSynthesizeClient{}, nil
		},
	}
	_, err = service.StreamSynthesize(
		ctx,
		"安全な回答です。",
		func([]byte) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if called {
		t.Fatal("provider must not be called for an already-canceled context")
	}
}

func TestStreamSynthesizeValidatesInputBeforeProviderCall(t *testing.T) {
	t.Parallel()

	called := false
	service := &CloudService{
		streamSynthesizeCall: func(
			context.Context,
		) (streamingSynthesizeClient, error) {
			called = true
			return &fakeStreamingSynthesizeClient{}, nil
		},
	}

	if _, err := service.StreamSynthesize(
		context.Background(),
		" ",
		func([]byte) error { return nil },
	); err == nil {
		t.Fatal("empty text was accepted")
	}
	if _, err := service.StreamSynthesize(
		context.Background(),
		strings.Repeat("あ", maxSpokenReplyRunes+1),
		func([]byte) error { return nil },
	); !errors.Is(err, ErrReplyLong) {
		t.Fatalf("long reply error = %v", err)
	}
	if _, err := service.StreamSynthesize(
		context.Background(),
		"安全な回答です。",
		nil,
	); err == nil {
		t.Fatal("nil chunk handler was accepted")
	}
	if called {
		t.Fatal("provider was called for invalid input")
	}
}

type streamingReceiveResult struct {
	response *texttospeechpb.StreamingSynthesizeResponse
	err      error
}

type fakeStreamingSynthesizeClient struct {
	sent         []*texttospeechpb.StreamingSynthesizeRequest
	sendErrorAt  int
	sendErr      error
	closeErr     error
	closeCalled  bool
	recvResults  []streamingReceiveResult
	receiveIndex int
}

func (f *fakeStreamingSynthesizeClient) Send(
	request *texttospeechpb.StreamingSynthesizeRequest,
) error {
	f.sent = append(f.sent, request)
	if f.sendErrorAt == len(f.sent) {
		return f.sendErr
	}
	return nil
}

func (f *fakeStreamingSynthesizeClient) Recv() (
	*texttospeechpb.StreamingSynthesizeResponse,
	error,
) {
	if f.receiveIndex >= len(f.recvResults) {
		return nil, io.EOF
	}
	result := f.recvResults[f.receiveIndex]
	f.receiveIndex++
	return result.response, result.err
}

func (f *fakeStreamingSynthesizeClient) CloseSend() error {
	f.closeCalled = true
	return f.closeErr
}

func cloudServiceWithStream(stream streamingSynthesizeClient) *CloudService {
	return &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		streamSynthesizeCall: func(
			context.Context,
		) (streamingSynthesizeClient, error) {
			return stream, nil
		},
	}
}
