package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func pairedStreamingService(
	streams ...*fakeStreamingRecognizeClient,
) *CloudService {
	var mutex sync.Mutex
	next := 0
	return &CloudService{
		recognizer:  "projects/project/locations/asia-northeast1/recognizers/_",
		speechModel: "chirp_3",
		streamRecognizeCall: func(context.Context) (streamingRecognizeClient, error) {
			mutex.Lock()
			defer mutex.Unlock()
			if next >= len(streams) {
				return nil, errors.New("unexpected stream")
			}
			stream := streams[next]
			next++
			return stream, nil
		},
	}
}

func pairedFinalStream(text string, confidence float32) *fakeStreamingRecognizeClient {
	return &fakeStreamingRecognizeClient{recv: []streamingRecognizeReceive{{
		response: &speechpb.StreamingRecognizeResponse{Results: []*speechpb.StreamingRecognitionResult{
			streamingTestResult(text, true, 0, confidence),
		}},
	}}}
}

func TestPairedPCM16AcceptsOnlyExactIndependentAgreement(t *testing.T) {
	baselineStream := pairedFinalStream(" 小さな 声です ", .91)
	weakStream := pairedFinalStream("小さな 声です", .89)
	enhancedStream := pairedFinalStream("小さな 声です", .87)
	service := pairedStreamingService(baselineStream, weakStream, enhancedStream)
	baseline := bytes.Repeat([]byte{1, 0}, 320)
	enhanced := bytes.Repeat([]byte{5, 0}, 320)
	text, confidence, err := service.TranscribePairedPCM16(
		context.Background(),
		baseline,
		enhanced,
	)
	if err != nil || text != "小さな 声です" || confidence != .87 {
		t.Fatalf("result=(%q,%f,%v)", text, confidence, err)
	}
	observed := map[byte]bool{}
	for _, stream := range []*fakeStreamingRecognizeClient{baselineStream, weakStream, enhancedStream} {
		if len(stream.sent) != 2 || len(stream.sent[1].GetAudio()) != 640 {
			t.Fatalf("stream requests = %#v", stream.sent)
		}
		observed[stream.sent[1].GetAudio()[0]] = true
	}
	if !observed[0] || !observed[1] || !observed[5] {
		t.Fatalf("baseline/strong views or zeroized weak view missing: %#v", observed)
	}
}

func TestPairedPCM16RejectsSubstitutionAndOneSidedSpeech(t *testing.T) {
	for _, test := range []struct {
		name     string
		baseline *fakeStreamingRecognizeClient
		weak     *fakeStreamingRecognizeClient
		enhanced *fakeStreamingRecognizeClient
	}{
		{
			name:     "substitution",
			baseline: pairedFinalStream("今日は休みます", .9),
			weak:     &fakeStreamingRecognizeClient{},
			enhanced: pairedFinalStream("今日は走ります", .99),
		},
		{
			name:     "enhanced only",
			baseline: &fakeStreamingRecognizeClient{},
			weak:     &fakeStreamingRecognizeClient{},
			enhanced: pairedFinalStream("補完された文", .99),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := pairedStreamingService(test.baseline, test.weak, test.enhanced)
			_, _, err := service.TranscribePairedPCM16(
				context.Background(),
				make([]byte, 640),
				make([]byte, 640),
			)
			if !errors.Is(err, ErrPairedRecognitionUnresolved) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPairedPCM16AcceptsEveryTwoOfThreeExactAgreement(t *testing.T) {
	for _, test := range []struct {
		name       string
		transcript [3]string
		confidence [3]float32
		want       float32
	}{
		{name: "baseline and weak", transcript: [3]string{"こんにちは", "こんにちは", "こんちは"}, confidence: [3]float32{.82, .79, .99}, want: .79},
		{name: "weak and enhanced", transcript: [3]string{"", "うん", "うん"}, confidence: [3]float32{0, .76, .91}, want: .76},
		{name: "baseline and enhanced", transcript: [3]string{"いや", "いま", "いや"}, confidence: [3]float32{.88, .95, .81}, want: .81},
	} {
		t.Run(test.name, func(t *testing.T) {
			streams := make([]*fakeStreamingRecognizeClient, 3)
			for index := range streams {
				if test.transcript[index] == "" {
					streams[index] = &fakeStreamingRecognizeClient{}
				} else {
					streams[index] = pairedFinalStream(test.transcript[index], test.confidence[index])
				}
			}
			service := pairedStreamingService(streams...)
			text, confidence, err := service.TranscribePairedPCM16(
				context.Background(),
				bytes.Repeat([]byte{1, 0}, 320),
				bytes.Repeat([]byte{3, 0}, 320),
			)
			if err != nil || text == "" || confidence != test.want {
				t.Fatalf("result=(%q,%f,%v)", text, confidence, err)
			}
		})
	}
}

func TestPairedPCM16RejectsProviderFailureEvenWhenOtherViewsAgree(t *testing.T) {
	failed := &fakeStreamingRecognizeClient{recv: []streamingRecognizeReceive{{
		err: status.Error(codes.Unavailable, "provider unavailable"),
	}}}
	service := pairedStreamingService(
		pairedFinalStream("こんにちは", .8),
		failed,
		pairedFinalStream("こんにちは", .9),
	)
	_, _, err := service.TranscribePairedPCM16(
		context.Background(),
		bytes.Repeat([]byte{1, 0}, 320),
		bytes.Repeat([]byte{2, 0}, 320),
	)
	if !errors.Is(err, ErrPairedRecognitionUnresolved) {
		t.Fatalf("provider failure escaped: %v", err)
	}
}

func TestWeakPCM16IsBoundedSampleAlignedObservationMix(t *testing.T) {
	baseline := []byte{0x00, 0x80, 0xff, 0x7f, 0xe8, 0x03}
	enhanced := []byte{0xff, 0x7f, 0x00, 0x80, 0xd0, 0x07}
	weak, err := deriveWeakPCM16(baseline, enhanced)
	if err != nil {
		t.Fatal(err)
	}
	want := []int16{-16384, 16383, 1250}
	for index := range want {
		offset := index * 2
		got := int16(uint16(weak[offset]) | uint16(weak[offset+1])<<8)
		if got != want[index] {
			t.Fatalf("sample %d = %d; want %d", index, got, want[index])
		}
	}
}

func TestPairedAgreementRejectsOneHundredThousandTokenCounterexamples(t *testing.T) {
	for index := 0; index < 100_000; index++ {
		baseline := "token-" + string(rune(0x3041+(index%80)))
		enhanced := baseline + " insertion"
		if pairedTranscriptsAgree(baseline, enhanced) ||
			pairedTranscriptsAgree(baseline, "deletion") ||
			pairedTranscriptsAgree(baseline, "substitution") {
			t.Fatalf("counterexample %d was accepted", index)
		}
		if text, agreed := threeViewTranscriptConsensus(baseline, enhanced, "substitution"); text != "" || len(agreed) != 0 {
			t.Fatalf("three-view counterexample %d was accepted", index)
		}
	}
}

func TestPairedAgreementRejectsInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff, 0xfe})
	if pairedTranscriptsAgree(invalid, invalid) {
		t.Fatal("invalid UTF-8 was accepted as paired recognition")
	}
}

func TestNewCloudServiceRejectsNonConversationModelBeforeClientInitialization(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"", "short", "latest_short", "latest_long", "long"} {
		model := model
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			service, err := NewCloudService(
				context.Background(),
				"project",
				"asia-northeast1",
				model,
				"ja-JP-Chirp3-HD-Kore",
			)
			if err == nil || service != nil {
				t.Fatalf("service=%v err=%v; want local model rejection", service, err)
			}
		})
	}
}

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

func TestTranscribeUsesOnlyConfiguredChirp3Model(t *testing.T) {
	t.Parallel()

	var calls int
	service := &CloudService{
		speechModel: "chirp_3",
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			if request.Config.Model != "chirp_3" {
				t.Fatalf("model = %q; want chirp_3", request.Config.Model)
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
			if request.Config.Features.MaxAlternatives != 1 ||
				request.Config.Features.EnableWordConfidence ||
				request.Config.Features.EnableWordTimeOffsets {
				t.Fatalf("reviewed features = %+v", request.Config.Features)
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

func TestTranscribePCM16UsesExplicitJapaneseChirp3Contract(t *testing.T) {
	t.Parallel()
	stream := &fakeStreamingRecognizeClient{recv: []streamingRecognizeReceive{
		{response: &speechpb.StreamingRecognizeResponse{
			Results: []*speechpb.StreamingRecognitionResult{
				streamingTestResult("小さな声", true, 0, 0.91),
			},
		}},
	}}
	service := streamingTestService(stream, "chirp_3")
	audio := make([]byte, maxStreamingPCMBytes+640)
	text, confidence, err := service.TranscribePCM16(
		context.Background(),
		audio,
	)
	if err != nil || text != "小さな声" || confidence != 0.91 {
		t.Fatalf("result=(%q,%f,%v)", text, confidence, err)
	}
	if len(stream.sent) != 3 ||
		len(stream.sent[1].GetAudio()) != maxStreamingPCMBytes ||
		len(stream.sent[2].GetAudio()) != 640 || stream.closeCalls != 1 {
		t.Fatalf(
			"stream requests=%d first=%d second=%d closes=%d",
			len(stream.sent),
			len(stream.sent[1].GetAudio()),
			len(stream.sent[2].GetAudio()),
			stream.closeCalls,
		)
	}
	if _, _, err := service.TranscribePCM16(
		context.Background(),
		[]byte{1},
	); !errors.Is(err, ErrNoSpeech) {
		t.Fatalf("odd PCM error = %v", err)
	}
}

func TestTranscribeReturnsProviderErrorWithoutRetry(t *testing.T) {
	t.Parallel()

	var calls int
	service := &CloudService{
		speechModel: "chirp_3",
		recognizeCall: func(
			_ context.Context,
			request *speechpb.RecognizeRequest,
		) (*speechpb.RecognizeResponse, error) {
			calls++
			if request.Config.Model != "chirp_3" {
				t.Fatalf("model = %q; want chirp_3", request.Config.Model)
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
		speechModel: "chirp_3",
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

func TestTranscribeRejectsNonConversationModelBeforeProviderCall(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"", "short", "latest_short", "latest_long", "long"} {
		model := model
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			var calls int
			service := &CloudService{
				speechModel: model,
				recognizeCall: func(
					context.Context,
					*speechpb.RecognizeRequest,
				) (*speechpb.RecognizeResponse, error) {
					calls++
					return recognizedResponse("unexpected", 1), nil
				},
			}
			if _, _, err := service.Transcribe(
				context.Background(),
				[]byte{1, 2},
			); err == nil {
				t.Fatal("unsupported model accepted")
			}
			if calls != 0 {
				t.Fatalf("provider calls = %d; want 0", calls)
			}
		})
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
