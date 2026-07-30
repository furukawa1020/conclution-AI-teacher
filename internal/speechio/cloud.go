package speechio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	speech "cloud.google.com/go/speech/apiv2"
	"cloud.google.com/go/speech/apiv2/speechpb"
	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"google.golang.org/api/option"
)

const (
	maxTranscriptRunes         = 12_000
	maxSpokenReplyRunes        = 1_200
	maxStreamingAudioChunkSize = 1 << 20
	maxStreamingAudioTotalSize = 16 << 20

	// StreamingAudioContentType describes the raw audio bytes returned by
	// StreamSynthesize. The stream has no container or file header.
	StreamingAudioContentType = "audio/L16"
	StreamingSampleRateHertz  = 24_000
	StreamingChannelCount     = 1
	StreamingBitsPerSample    = 16
)

var (
	ErrNoSpeech               = errors.New("no speech was recognized")
	ErrTranscriptLong         = errors.New("recognized speech is too long")
	ErrReplyLong              = errors.New("spoken reply is too long")
	ErrNoStreamingAudio       = errors.New("streaming speech synthesis returned no audio")
	ErrEmptyStreamingChunk    = errors.New("streaming speech synthesis returned an empty audio chunk")
	ErrStreamingChunkTooLarge = errors.New("streaming speech synthesis chunk is too large")
	ErrStreamingAudioTooLarge = errors.New("streaming speech synthesis audio is too large")
)

// Service is the deliberately narrow audio boundary for KOTAE. Raw audio is
// accepted only as a method argument and is never logged or persisted here.
type Service interface {
	Transcribe(ctx context.Context, audio []byte) (string, float32, error)
	Synthesize(ctx context.Context, text string) ([]byte, string, error)
}

// StreamChunkHandler consumes one raw, signed 16-bit little-endian PCM chunk.
// The byte slice is valid only for the duration of the call. Returning an error
// aborts synthesis.
type StreamChunkHandler func(audio []byte) error

// StreamingService adds bounded streaming synthesis without widening the
// existing Service interface, so existing implementations remain compatible.
type StreamingService interface {
	Service
	StreamSynthesize(
		ctx context.Context,
		text string,
		onChunk StreamChunkHandler,
	) (string, error)
}

type streamingSynthesizeClient interface {
	Send(*texttospeechpb.StreamingSynthesizeRequest) error
	Recv() (*texttospeechpb.StreamingSynthesizeResponse, error)
	CloseSend() error
}

type CloudService struct {
	speechClient  *speech.Client
	ttsClient     *texttospeech.Client
	recognizer    string
	speechModel   string
	voiceName     string
	recognizeCall func(
		context.Context,
		*speechpb.RecognizeRequest,
	) (*speechpb.RecognizeResponse, error)
	streamRecognizeCall  func(context.Context) (streamingRecognizeClient, error)
	streamSynthesizeCall func(context.Context) (streamingSynthesizeClient, error)
}

func NewCloudService(
	ctx context.Context,
	projectID string,
	location string,
	speechModel string,
	voiceName string,
) (*CloudService, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(location) == "" {
		return nil, errors.New("project and speech location are required")
	}

	speechClient, err := speech.NewClient(
		ctx,
		option.WithEndpoint(location+"-speech.googleapis.com:443"),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize regional speech client: %w", err)
	}
	ttsClient, err := texttospeech.NewClient(
		ctx,
		option.WithEndpoint(location+"-texttospeech.googleapis.com:443"),
	)
	if err != nil {
		_ = speechClient.Close()
		return nil, fmt.Errorf("initialize regional text-to-speech client: %w", err)
	}

	service := &CloudService{
		speechClient: speechClient,
		ttsClient:    ttsClient,
		recognizer: fmt.Sprintf(
			"projects/%s/locations/%s/recognizers/_",
			projectID,
			location,
		),
		speechModel: speechModel,
		voiceName:   voiceName,
	}
	service.recognizeCall = func(
		callContext context.Context,
		request *speechpb.RecognizeRequest,
	) (*speechpb.RecognizeResponse, error) {
		return speechClient.Recognize(callContext, request)
	}
	service.streamRecognizeCall = func(
		callContext context.Context,
	) (streamingRecognizeClient, error) {
		return speechClient.StreamingRecognize(callContext)
	}
	service.streamSynthesizeCall = func(
		callContext context.Context,
	) (streamingSynthesizeClient, error) {
		return ttsClient.StreamingSynthesize(callContext)
	}
	return service, nil
}

func (s *CloudService) Close() error {
	return errors.Join(s.speechClient.Close(), s.ttsClient.Close())
}

func (s *CloudService) Transcribe(
	ctx context.Context,
	audio []byte,
) (string, float32, error) {
	if len(audio) == 0 {
		return "", 0, ErrNoSpeech
	}

	response, err := s.recognize(ctx, audio, s.speechModel)
	if err != nil {
		return "", 0, fmt.Errorf("regional speech recognition failed: %w", err)
	}

	transcript, confidence := recognizedText(response)
	if transcript == "" {
		return "", 0, ErrNoSpeech
	}
	if utf8.RuneCountInString(transcript) > maxTranscriptRunes {
		return "", 0, ErrTranscriptLong
	}
	return transcript, confidence, nil
}

func (s *CloudService) recognize(
	ctx context.Context,
	audio []byte,
	model string,
) (*speechpb.RecognizeResponse, error) {
	return s.recognizeCall(ctx, &speechpb.RecognizeRequest{
		Recognizer: s.recognizer,
		Config: &speechpb.RecognitionConfig{
			DecodingConfig: &speechpb.RecognitionConfig_AutoDecodingConfig{
				AutoDecodingConfig: &speechpb.AutoDetectDecodingConfig{},
			},
			Model:         model,
			LanguageCodes: []string{"ja-JP"},
			Features: &speechpb.RecognitionFeatures{
				EnableAutomaticPunctuation: true,
				EnableWordConfidence:       false,
				EnableWordTimeOffsets:      false,
			},
		},
		AudioSource: &speechpb.RecognizeRequest_Content{Content: audio},
	})
}

func recognizedText(response *speechpb.RecognizeResponse) (string, float32) {
	if response == nil {
		return "", 0
	}

	var text strings.Builder
	var minimumPositiveConfidence float32
	hasPositiveConfidence := false
	for _, result := range response.Results {
		if result == nil || len(result.Alternatives) == 0 || result.Alternatives[0] == nil {
			continue
		}
		alternative := result.Alternatives[0]
		fragment := strings.TrimSpace(alternative.Transcript)
		if fragment == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte(' ')
		}
		text.WriteString(fragment)
		if alternative.Confidence > 0 {
			if !hasPositiveConfidence ||
				alternative.Confidence < minimumPositiveConfidence {
				minimumPositiveConfidence = alternative.Confidence
			}
			hasPositiveConfidence = true
		}
	}

	// Speech recognizers may omit utterance confidence and encode it as zero.
	// Preserve zero when no fragment reports confidence. When confidence is
	// available, use the minimum across nonempty top fragments so one uncertain
	// section cannot be hidden by averaging it with confident sections.
	if !hasPositiveConfidence {
		return strings.TrimSpace(text.String()), 0
	}
	return strings.TrimSpace(text.String()), minimumPositiveConfidence
}

func (s *CloudService) Synthesize(
	ctx context.Context,
	text string,
) ([]byte, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", errors.New("spoken reply is empty")
	}
	if utf8.RuneCountInString(text) > maxSpokenReplyRunes {
		return nil, "", ErrReplyLong
	}

	response, err := s.ttsClient.SynthesizeSpeech(ctx, &texttospeechpb.SynthesizeSpeechRequest{
		Input: &texttospeechpb.SynthesisInput{
			InputSource: &texttospeechpb.SynthesisInput_Text{Text: text},
		},
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: "ja-JP",
			Name:         s.voiceName,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_MP3,
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("regional speech synthesis failed: %w", err)
	}
	if response == nil || len(response.AudioContent) == 0 {
		return nil, "", errors.New("regional speech synthesis returned no audio")
	}
	return response.AudioContent, "audio/mpeg", nil
}

// StreamSynthesize synthesizes an already-approved plain-text reply with the
// configured voice and delivers raw PCM audio as the provider produces it.
//
// Chunks are headerless signed 16-bit little-endian, 24 kHz, mono PCM. A
// non-nil error invalidates the stream, including audio delivered before a
// late provider or callback failure.
func (s *CloudService) StreamSynthesize(
	ctx context.Context,
	text string,
	onChunk StreamChunkHandler,
) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("spoken reply is empty")
	}
	if utf8.RuneCountInString(text) > maxSpokenReplyRunes {
		return "", ErrReplyLong
	}
	if onChunk == nil {
		return "", errors.New("streaming speech synthesis requires a chunk handler")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("streaming speech synthesis canceled: %w", err)
	}
	if s.streamSynthesizeCall == nil {
		return "", errors.New("streaming speech synthesis is unavailable")
	}

	streamContext, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	stream, err := s.streamSynthesizeCall(streamContext)
	if err != nil {
		return "", fmt.Errorf("start regional streaming speech synthesis: %w", err)
	}
	if stream == nil {
		return "", errors.New("regional streaming speech synthesis returned no stream")
	}

	configRequest, inputRequest := streamingSynthesizeRequests(text, s.voiceName)
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("send regional streaming speech configuration: %w", err)
	}
	if err := stream.Send(configRequest); err != nil {
		return "", fmt.Errorf("send regional streaming speech configuration: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("send regional streaming speech input: %w", err)
	}
	if err := stream.Send(inputRequest); err != nil {
		return "", fmt.Errorf("send regional streaming speech input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("close regional streaming speech input: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return "", fmt.Errorf("close regional streaming speech input: %w", err)
	}
	if err := receiveStreamingAudio(ctx, stream, onChunk); err != nil {
		return "", err
	}
	return StreamingAudioContentType, nil
}

func streamingSynthesizeRequests(
	text string,
	voiceName string,
) (*texttospeechpb.StreamingSynthesizeRequest, *texttospeechpb.StreamingSynthesizeRequest) {
	configRequest := &texttospeechpb.StreamingSynthesizeRequest{
		StreamingRequest: &texttospeechpb.StreamingSynthesizeRequest_StreamingConfig{
			StreamingConfig: &texttospeechpb.StreamingSynthesizeConfig{
				Voice: &texttospeechpb.VoiceSelectionParams{
					LanguageCode: "ja-JP",
					Name:         voiceName,
				},
				StreamingAudioConfig: &texttospeechpb.StreamingAudioConfig{
					AudioEncoding:   texttospeechpb.AudioEncoding_PCM,
					SampleRateHertz: StreamingSampleRateHertz,
				},
			},
		},
	}
	inputRequest := &texttospeechpb.StreamingSynthesizeRequest{
		StreamingRequest: &texttospeechpb.StreamingSynthesizeRequest_Input{
			Input: &texttospeechpb.StreamingSynthesisInput{
				InputSource: &texttospeechpb.StreamingSynthesisInput_Text{
					Text: text,
				},
			},
		},
	}
	return configRequest, inputRequest
}

func receiveStreamingAudio(
	ctx context.Context,
	stream streamingSynthesizeClient,
	onChunk StreamChunkHandler,
) error {
	totalBytes := 0
	chunkCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("receive regional streaming speech audio: %w", err)
		}

		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if chunkCount == 0 {
				return ErrNoStreamingAudio
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive regional streaming speech audio: %w", err)
		}
		if response == nil || len(response.AudioContent) == 0 {
			return ErrEmptyStreamingChunk
		}

		chunkSize := len(response.AudioContent)
		if chunkSize > maxStreamingAudioChunkSize {
			return ErrStreamingChunkTooLarge
		}
		if chunkSize > maxStreamingAudioTotalSize-totalBytes {
			return ErrStreamingAudioTooLarge
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("deliver regional streaming speech audio: %w", err)
		}
		if err := onChunk(response.AudioContent); err != nil {
			return fmt.Errorf("deliver regional streaming speech audio: %w", err)
		}

		totalBytes += chunkSize
		chunkCount++
	}
}
