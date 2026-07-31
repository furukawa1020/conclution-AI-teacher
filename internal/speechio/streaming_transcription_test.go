package speechio

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
)

func TestOpenStreamingTranscriptionSendsExplicitPCMConfigFirst(t *testing.T) {
	t.Parallel()
	stream := &fakeStreamingRecognizeClient{}
	service := streamingTestService(stream, "long")
	session, err := service.OpenStreamingTranscription(context.Background())
	if err != nil || session == nil {
		t.Fatalf("open: session=%v err=%v", session, err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("sent=%d want config only", len(stream.sent))
	}
	request := stream.sent[0]
	config := request.GetStreamingConfig()
	if request.Recognizer != service.recognizer ||
		len(request.GetAudio()) != 0 ||
		config == nil ||
		config.Config == nil ||
		config.StreamingFeatures == nil {
		t.Fatalf("bad initial request: %+v", request)
	}
	explicit := config.Config.GetExplicitDecodingConfig()
	if explicit == nil ||
		explicit.Encoding != speechpb.ExplicitDecodingConfig_LINEAR16 ||
		explicit.SampleRateHertz != StreamingInputSampleRateHertz ||
		explicit.AudioChannelCount != StreamingInputChannelCount {
		t.Fatalf("decoding=%+v", explicit)
	}
	if config.Config.Model != "long" ||
		len(config.Config.LanguageCodes) != 1 ||
		config.Config.LanguageCodes[0] != "ja-JP" ||
		config.Config.Features == nil ||
		!config.Config.Features.EnableAutomaticPunctuation ||
		config.Config.Features.MaxAlternatives != 1 ||
		!config.StreamingFeatures.InterimResults ||
		!config.StreamingFeatures.EnableVoiceActivityEvents ||
		config.StreamingFeatures.EndpointingSensitivity !=
			speechpb.StreamingRecognitionFeatures_ENDPOINTING_SENSITIVITY_UNSPECIFIED {
		t.Fatalf("recognition config=%+v", config)
	}
}

func TestStreamingRecognitionConfigUsesReviewedLongModelWithoutUnsupportedEndpointing(t *testing.T) {
	t.Parallel()
	long := streamingRecognitionConfigRequest("recognizer", " long ").
		GetStreamingConfig()
	if long.Config.Model != "long" ||
		long.StreamingFeatures.EndpointingSensitivity !=
			speechpb.StreamingRecognitionFeatures_ENDPOINTING_SENSITIVITY_UNSPECIFIED {
		t.Fatalf("long config=%+v", long)
	}
}

func TestOpenStreamingTranscriptionRejectsNonConversationModelBeforeProviderCall(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"", "short", "latest_short", "latest_long", "chirp_3"} {
		model := model
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			var calls int
			service := &CloudService{
				speechModel: model,
				streamRecognizeCall: func(
					context.Context,
				) (streamingRecognizeClient, error) {
					calls++
					return &fakeStreamingRecognizeClient{}, nil
				},
			}
			if _, err := service.OpenStreamingTranscription(
				context.Background(),
			); err == nil {
				t.Fatal("unsupported streaming model accepted")
			}
			if calls != 0 {
				t.Fatalf("provider calls = %d; want 0", calls)
			}
		})
	}
}

func TestStreamingTranscriptionSendPCMBoundsAndClose(t *testing.T) {
	t.Parallel()
	stream := &fakeStreamingRecognizeClient{}
	session := openStreamingTestSession(t, stream)
	if err := session.SendPCM(make([]byte, maxStreamingPCMBytes)); err != nil {
		t.Fatal(err)
	}
	request := stream.sent[1]
	if request.GetStreamingConfig() != nil ||
		len(request.GetAudio()) != maxStreamingPCMBytes ||
		request.Recognizer != "" {
		t.Fatalf("audio request=%+v", request)
	}
	for _, test := range []struct {
		audio []byte
		want  error
	}{
		{nil, ErrEmptyStreamingPCM},
		{[]byte{1}, ErrUnalignedPCM},
		{make([]byte, maxStreamingPCMBytes+2), ErrStreamingPCMTooLarge},
	} {
		if err := session.SendPCM(test.audio); !errors.Is(err, test.want) {
			t.Fatalf("SendPCM error=%v want=%v", err, test.want)
		}
	}
	if err := session.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := session.CloseSend(); err != nil || stream.closeCalls != 1 {
		t.Fatalf("idempotent close err=%v calls=%d", err, stream.closeCalls)
	}
	if err := session.SendPCM([]byte{1, 2}); !errors.Is(
		err,
		ErrStreamingTranscriptionClosed,
	) {
		t.Fatalf("send after close=%v", err)
	}
}

func TestStreamingTranscriptionEventsMapTopAlternativeAndActivity(t *testing.T) {
	t.Parallel()
	events, err := streamingTranscriptionEvents(&speechpb.StreamingRecognizeResponse{
		Results: []*speechpb.StreamingRecognitionResult{
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: " 途中 ", Confidence: .7},
					{Transcript: "not top", Confidence: .99},
				},
				Stability: .8,
			},
			streamingTestResult("確定", true, 0, .95),
		},
	})
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events[0].Kind != StreamingTranscriptionInterim ||
		events[0].Text != "途中" ||
		events[0].Stability != .8 ||
		events[0].Confidence != .7 ||
		events[1].Kind != StreamingTranscriptionFinal ||
		events[1].Text != "確定" ||
		events[1].Confidence != .95 {
		t.Fatalf("mapped events=%+v", events)
	}
	for _, test := range []struct {
		provider speechpb.StreamingRecognizeResponse_SpeechEventType
		kind     StreamingTranscriptionEventKind
		end      bool
	}{
		{speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_BEGIN, StreamingTranscriptionSpeechBegin, false},
		{speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_END, StreamingTranscriptionSpeechEnd, false},
		{speechpb.StreamingRecognizeResponse_END_OF_SINGLE_UTTERANCE, StreamingTranscriptionSpeechEnd, true},
	} {
		mapped, err := streamingTranscriptionEvents(&speechpb.StreamingRecognizeResponse{
			SpeechEventType: test.provider,
		})
		if err != nil || len(mapped) != 1 ||
			mapped[0].Kind != test.kind ||
			mapped[0].EndOfUtterance != test.end {
			t.Fatalf("speech event=%+v err=%v", mapped, err)
		}
	}
}

func TestStreamingTranscriptionEventsFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		response *speechpb.StreamingRecognizeResponse
		want     error
	}{
		{nil, ErrEmptyStreamingRecognitionResponse},
		{&speechpb.StreamingRecognizeResponse{}, ErrEmptyStreamingRecognitionResponse},
		{
			&speechpb.StreamingRecognizeResponse{
				SpeechEventType: speechpb.StreamingRecognizeResponse_SpeechEventType(99),
			},
			ErrUnsupportedStreamingSpeechEvent,
		},
		{
			&speechpb.StreamingRecognizeResponse{
				Results: []*speechpb.StreamingRecognitionResult{
					streamingTestResult(strings.Repeat("あ", maxTranscriptRunes+1), true, 0, .9),
				},
			},
			ErrTranscriptLong,
		},
	}
	for _, test := range tests {
		_, err := streamingTranscriptionEvents(test.response)
		if !errors.Is(err, test.want) {
			t.Fatalf("error=%v want=%v", err, test.want)
		}
	}
}

func TestStreamingTranscriptionRecvQueuesThenReturnsEOF(t *testing.T) {
	t.Parallel()
	stream := &fakeStreamingRecognizeClient{
		recv: []streamingRecognizeReceive{
			{response: &speechpb.StreamingRecognizeResponse{
				Results: []*speechpb.StreamingRecognitionResult{
					streamingTestResult("途中", false, .5, 0),
					streamingTestResult("確定", true, 0, .9),
				},
			}},
			{response: &speechpb.StreamingRecognizeResponse{
				SpeechEventType: speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_END,
			}},
			{err: io.EOF},
		},
	}
	session := openStreamingTestSession(t, stream)
	for _, kind := range []StreamingTranscriptionEventKind{
		StreamingTranscriptionInterim,
		StreamingTranscriptionFinal,
		StreamingTranscriptionSpeechEnd,
	} {
		event, err := session.RecvEvent()
		if err != nil || event.Kind != kind {
			t.Fatalf("event=%+v err=%v want=%s", event, err, kind)
		}
	}
	if stream.recvCalls != 2 {
		t.Fatalf("queued result caused Recv: calls=%d", stream.recvCalls)
	}
	if _, err := session.RecvEvent(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF=%v", err)
	}
	if _, err := session.RecvEvent(); !errors.Is(err, io.EOF) {
		t.Fatalf("repeat EOF=%v", err)
	}
}

func TestStreamingTranscriptionCancellationAndTransportFailures(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	stream := &fakeStreamingRecognizeClient{}
	session, err := streamingTestService(stream, "long").
		OpenStreamingTranscription(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := session.SendPCM([]byte{1, 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("send cancel=%v", err)
	}
	if _, err := session.RecvEvent(); !errors.Is(err, context.Canceled) {
		t.Fatalf("recv cancel=%v", err)
	}

	startErr := errors.New("start failed")
	service := streamingTestService(nil, "long")
	service.streamRecognizeCall = func(context.Context) (streamingRecognizeClient, error) {
		return nil, startErr
	}
	if _, err := service.OpenStreamingTranscription(
		context.Background(),
	); !errors.Is(err, startErr) {
		t.Fatalf("start error=%v", err)
	}
	service.streamRecognizeCall = func(context.Context) (streamingRecognizeClient, error) {
		return nil, nil
	}
	if _, err := service.OpenStreamingTranscription(
		context.Background(),
	); err == nil {
		t.Fatal("nil provider stream accepted")
	}
}

type streamingRecognizeReceive struct {
	response *speechpb.StreamingRecognizeResponse
	err      error
}

type fakeStreamingRecognizeClient struct {
	sent        []*speechpb.StreamingRecognizeRequest
	sendErrorAt int
	sendErr     error
	closeErr    error
	closeCalls  int
	recv        []streamingRecognizeReceive
	recvIndex   int
	recvCalls   int
}

func (f *fakeStreamingRecognizeClient) Send(request *speechpb.StreamingRecognizeRequest) error {
	f.sent = append(f.sent, request)
	if f.sendErrorAt == len(f.sent) {
		return f.sendErr
	}
	return nil
}

func (f *fakeStreamingRecognizeClient) Recv() (*speechpb.StreamingRecognizeResponse, error) {
	f.recvCalls++
	if f.recvIndex >= len(f.recv) {
		return nil, io.EOF
	}
	result := f.recv[f.recvIndex]
	f.recvIndex++
	return result.response, result.err
}

func (f *fakeStreamingRecognizeClient) CloseSend() error {
	f.closeCalls++
	return f.closeErr
}

func streamingTestService(
	stream streamingRecognizeClient,
	model string,
) *CloudService {
	return &CloudService{
		recognizer:  "projects/project/locations/asia-northeast1/recognizers/_",
		speechModel: model,
		streamRecognizeCall: func(
			context.Context,
		) (streamingRecognizeClient, error) {
			return stream, nil
		},
	}
}

func openStreamingTestSession(
	t *testing.T,
	stream streamingRecognizeClient,
) StreamingTranscriptionSession {
	t.Helper()
	session, err := streamingTestService(stream, "long").
		OpenStreamingTranscription(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func streamingTestResult(
	text string,
	final bool,
	stability float32,
	confidence float32,
) *speechpb.StreamingRecognitionResult {
	return &speechpb.StreamingRecognitionResult{
		Alternatives: []*speechpb.SpeechRecognitionAlternative{
			{Transcript: text, Confidence: confidence},
		},
		IsFinal:   final,
		Stability: stability,
	}
}
