package speechio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	speech "cloud.google.com/go/speech/apiv2"
	"cloud.google.com/go/speech/apiv2/speechpb"
	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"google.golang.org/api/option"
)

const (
	maxTranscriptRunes = 12_000
	maxSpokenReplyRunes = 1_200
)

var (
	ErrNoSpeech       = errors.New("no speech was recognized")
	ErrTranscriptLong = errors.New("recognized speech is too long")
	ErrReplyLong      = errors.New("spoken reply is too long")
)

// Service is the deliberately narrow audio boundary for KOTAE. Raw audio is
// accepted only as a method argument and is never logged or persisted here.
type Service interface {
	Transcribe(ctx context.Context, audio []byte) (string, float32, error)
	Synthesize(ctx context.Context, text string) ([]byte, string, error)
}

type CloudService struct {
	speechClient *speech.Client
	ttsClient    *texttospeech.Client
	recognizer   string
	speechModel  string
	voiceName    string
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

	return &CloudService{
		speechClient: speechClient,
		ttsClient:    ttsClient,
		recognizer: fmt.Sprintf(
			"projects/%s/locations/%s/recognizers/_",
			projectID,
			location,
		),
		speechModel: speechModel,
		voiceName:   voiceName,
	}, nil
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

	response, err := s.speechClient.Recognize(ctx, &speechpb.RecognizeRequest{
		Recognizer: s.recognizer,
		Config: &speechpb.RecognitionConfig{
			DecodingConfig: &speechpb.RecognitionConfig_AutoDecodingConfig{
				AutoDecodingConfig: &speechpb.AutoDetectDecodingConfig{},
			},
			Model:         s.speechModel,
			LanguageCodes: []string{"ja-JP"},
			Features: &speechpb.RecognitionFeatures{
				EnableAutomaticPunctuation: true,
				EnableWordConfidence:       false,
				EnableWordTimeOffsets:      false,
			},
		},
		AudioSource: &speechpb.RecognizeRequest_Content{Content: audio},
	})
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

func recognizedText(response *speechpb.RecognizeResponse) (string, float32) {
	if response == nil {
		return "", 0
	}

	var text strings.Builder
	var confidenceTotal float32
	var confidenceCount int
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
			confidenceTotal += alternative.Confidence
			confidenceCount++
		}
	}

	confidence := float32(0)
	if confidenceCount > 0 {
		confidence = confidenceTotal / float32(confidenceCount)
	}
	return strings.TrimSpace(text.String()), confidence
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
