package speechio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"cloud.google.com/go/speech/apiv2/speechpb"
)

const (
	StreamingInputSampleRateHertz = 16_000
	StreamingInputChannelCount    = 1
	StreamingInputBitsPerSample   = 16
	maxStreamingPCMBytes          = 15 * 1024
)

var (
	ErrEmptyStreamingPCM    = errors.New("streaming PCM audio is empty")
	ErrUnalignedPCM         = errors.New("streaming PCM audio must contain complete 16-bit samples")
	ErrStreamingPCMTooLarge = errors.New(
		"streaming PCM audio exceeds the per-message limit",
	)
	ErrStreamingTranscriptionClosed = errors.New(
		"streaming transcription input is closed",
	)
	ErrEmptyStreamingRecognitionResponse = errors.New(
		"streaming recognition returned an empty response",
	)
	ErrUnsupportedStreamingSpeechEvent = errors.New(
		"streaming recognition returned an unsupported speech event",
	)
)

type StreamingTranscriptionEventKind string

const (
	StreamingTranscriptionInterim     StreamingTranscriptionEventKind = "interim"
	StreamingTranscriptionFinal       StreamingTranscriptionEventKind = "final"
	StreamingTranscriptionSpeechBegin StreamingTranscriptionEventKind = "speech_begin"
	StreamingTranscriptionSpeechEnd   StreamingTranscriptionEventKind = "speech_end"
)

// StreamingTranscriptionEvent contains only the top recognition hypothesis.
// A zero Stability or Confidence means the provider did not supply that value.
// No transcript is logged or persisted by this package.
type StreamingTranscriptionEvent struct {
	Kind           StreamingTranscriptionEventKind
	Text           string
	Stability      float32
	Confidence     float32
	EndOfUtterance bool
}

// StreamingTranscriptionSession is tied to the context passed to
// OpenStreamingTranscription. One goroutine may call SendPCM while another
// calls RecvEvent. Calls to SendPCM are serialized, as are calls to RecvEvent.
type StreamingTranscriptionSession interface {
	SendPCM(audio []byte) error
	CloseSend() error
	RecvEvent() (StreamingTranscriptionEvent, error)
}

type streamingRecognizeClient interface {
	Send(*speechpb.StreamingRecognizeRequest) error
	Recv() (*speechpb.StreamingRecognizeResponse, error)
	CloseSend() error
}

type cloudStreamingTranscriptionSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream streamingRecognizeClient

	sendMu     sync.Mutex
	sendClosed bool
	closeErr   error

	recvMu      sync.Mutex
	pending     []StreamingTranscriptionEvent
	finalRunes  int
	recvDone    bool
	terminalErr error
}

// OpenStreamingTranscription starts a regional Speech-to-Text V2 stream and
// sends its configuration before returning the session. Audio sent afterward
// must be headerless signed 16-bit little-endian, 16 kHz, mono PCM.
func (s *CloudService) OpenStreamingTranscription(
	ctx context.Context,
) (StreamingTranscriptionSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("streaming transcription canceled: %w", err)
	}
	if err := validateConversationSpeechModel(s.speechModel); err != nil {
		return nil, err
	}
	if s.streamRecognizeCall == nil {
		return nil, errors.New("streaming transcription is unavailable")
	}

	streamContext, cancelStream := context.WithCancel(ctx)
	stream, err := s.streamRecognizeCall(streamContext)
	if err != nil {
		cancelStream()
		return nil, fmt.Errorf("start regional streaming transcription: %w", err)
	}
	if stream == nil {
		cancelStream()
		return nil, errors.New("regional streaming transcription returned no stream")
	}

	session := &cloudStreamingTranscriptionSession{
		ctx:    streamContext,
		cancel: cancelStream,
		stream: stream,
	}
	if err := session.ctx.Err(); err != nil {
		cancelStream()
		return nil, fmt.Errorf("send regional streaming transcription configuration: %w", err)
	}
	if err := stream.Send(streamingRecognitionConfigRequest(
		s.recognizer,
		s.speechModel,
	)); err != nil {
		cancelStream()
		return nil, fmt.Errorf("send regional streaming transcription configuration: %w", err)
	}
	return session, nil
}

func streamingRecognitionConfigRequest(
	recognizer string,
	model string,
) *speechpb.StreamingRecognizeRequest {
	streamingFeatures := &speechpb.StreamingRecognitionFeatures{
		EnableVoiceActivityEvents: true,
		InterimResults:            true,
	}

	return &speechpb.StreamingRecognizeRequest{
		Recognizer: recognizer,
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: &speechpb.RecognitionConfig{
					DecodingConfig: &speechpb.RecognitionConfig_ExplicitDecodingConfig{
						ExplicitDecodingConfig: &speechpb.ExplicitDecodingConfig{
							Encoding:          speechpb.ExplicitDecodingConfig_LINEAR16,
							SampleRateHertz:   StreamingInputSampleRateHertz,
							AudioChannelCount: StreamingInputChannelCount,
						},
					},
					Model:         strings.TrimSpace(model),
					LanguageCodes: []string{"ja-JP"},
					Features: &speechpb.RecognitionFeatures{
						EnableAutomaticPunctuation: true,
						EnableWordConfidence:       false,
						EnableWordTimeOffsets:      false,
						MaxAlternatives:            1,
					},
				},
				StreamingFeatures: streamingFeatures,
			},
		},
	}
}

func (s *cloudStreamingTranscriptionSession) SendPCM(audio []byte) error {
	if len(audio) == 0 {
		return ErrEmptyStreamingPCM
	}
	if len(audio)%2 != 0 {
		return ErrUnalignedPCM
	}
	if len(audio) > maxStreamingPCMBytes {
		return ErrStreamingPCMTooLarge
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return ErrStreamingTranscriptionClosed
	}
	if err := s.ctx.Err(); err != nil {
		return fmt.Errorf("send regional streaming transcription audio: %w", err)
	}
	if err := s.stream.Send(&speechpb.StreamingRecognizeRequest{
		StreamingRequest: &speechpb.StreamingRecognizeRequest_Audio{
			Audio: audio,
		},
	}); err != nil {
		s.sendClosed = true
		s.cancel()
		return fmt.Errorf("send regional streaming transcription audio: %w", err)
	}
	return nil
}

func (s *cloudStreamingTranscriptionSession) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return s.closeErr
	}
	s.sendClosed = true
	if err := s.ctx.Err(); err != nil {
		s.closeErr = fmt.Errorf("close regional streaming transcription input: %w", err)
		return s.closeErr
	}
	if err := s.stream.CloseSend(); err != nil {
		s.cancel()
		s.closeErr = fmt.Errorf("close regional streaming transcription input: %w", err)
	}
	return s.closeErr
}

func (s *cloudStreamingTranscriptionSession) RecvEvent() (
	StreamingTranscriptionEvent,
	error,
) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	if s.recvDone {
		return StreamingTranscriptionEvent{}, s.terminalErr
	}
	if err := s.ctx.Err(); err != nil {
		return s.finishReceive(fmt.Errorf(
			"receive regional streaming transcription event: %w",
			err,
		))
	}
	if len(s.pending) > 0 {
		event := s.pending[0]
		s.pending = s.pending[1:]
		return event, nil
	}

	for {
		response, err := s.stream.Recv()
		if errors.Is(err, io.EOF) {
			return s.finishReceive(io.EOF)
		}
		if err != nil {
			return s.finishReceive(fmt.Errorf(
				"receive regional streaming transcription event: %w",
				err,
			))
		}
		if err := s.ctx.Err(); err != nil {
			return s.finishReceive(fmt.Errorf(
				"receive regional streaming transcription event: %w",
				err,
			))
		}
		events, err := streamingTranscriptionEvents(response)
		if err != nil {
			return s.finishReceive(err)
		}
		if err := s.enforceFinalTranscriptBound(events); err != nil {
			return s.finishReceive(err)
		}
		event := events[0]
		s.pending = append(s.pending, events[1:]...)
		return event, nil
	}
}

func (s *cloudStreamingTranscriptionSession) enforceFinalTranscriptBound(
	events []StreamingTranscriptionEvent,
) error {
	additionalRunes := 0
	for _, event := range events {
		if event.Kind == StreamingTranscriptionFinal {
			additionalRunes += utf8.RuneCountInString(event.Text)
		}
	}
	if additionalRunes > maxTranscriptRunes-s.finalRunes {
		return ErrTranscriptLong
	}
	s.finalRunes += additionalRunes
	return nil
}

func (s *cloudStreamingTranscriptionSession) finishReceive(err error) (
	StreamingTranscriptionEvent,
	error,
) {
	s.recvDone = true
	s.terminalErr = err
	s.cancel()
	return StreamingTranscriptionEvent{}, err
}

func streamingTranscriptionEvents(
	response *speechpb.StreamingRecognizeResponse,
) ([]StreamingTranscriptionEvent, error) {
	if response == nil {
		return nil, ErrEmptyStreamingRecognitionResponse
	}
	events := make([]StreamingTranscriptionEvent, 0, len(response.Results)+1)
	switch response.SpeechEventType {
	case speechpb.StreamingRecognizeResponse_SPEECH_EVENT_TYPE_UNSPECIFIED:
	case speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_BEGIN:
		events = append(events, StreamingTranscriptionEvent{
			Kind: StreamingTranscriptionSpeechBegin,
		})
	case speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_END:
		events = append(events, StreamingTranscriptionEvent{
			Kind: StreamingTranscriptionSpeechEnd,
		})
	case speechpb.StreamingRecognizeResponse_END_OF_SINGLE_UTTERANCE:
		events = append(events, StreamingTranscriptionEvent{
			Kind:           StreamingTranscriptionSpeechEnd,
			EndOfUtterance: true,
		})
	default:
		return nil, ErrUnsupportedStreamingSpeechEvent
	}
	for _, result := range response.Results {
		if result == nil ||
			len(result.Alternatives) == 0 ||
			result.Alternatives[0] == nil {
			continue
		}
		alternative := result.Alternatives[0]
		transcript := strings.TrimSpace(alternative.Transcript)
		if transcript == "" {
			continue
		}
		if utf8.RuneCountInString(transcript) > maxTranscriptRunes {
			return nil, ErrTranscriptLong
		}
		kind := StreamingTranscriptionInterim
		if result.IsFinal {
			kind = StreamingTranscriptionFinal
		}
		events = append(events, StreamingTranscriptionEvent{
			Kind:       kind,
			Text:       transcript,
			Stability:  result.Stability,
			Confidence: alternative.Confidence,
		})
	}
	if len(events) == 0 {
		return nil, ErrEmptyStreamingRecognitionResponse
	}
	return events, nil
}
