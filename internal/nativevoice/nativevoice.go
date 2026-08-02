// Package nativevoice provides a deliberately narrow, server-side boundary for
// Vertex AI Gemini Live native-audio sessions. It never logs or persists audio,
// transcripts, prompts, or provider error bodies.
package nativevoice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	DefaultModel     = "gemini-live-2.5-flash-native-audio"
	DefaultLocation  = "global"
	DefaultVoice     = "Aoede"
	APIVersion       = "v1"
	RouteNativeAudio = "native_audio"

	InputSampleRateHertz  = 16_000
	OutputSampleRateHertz = 24_000
	ChannelCount          = 1
	BitsPerSample         = 16
	InputFrameDuration    = 20 * time.Millisecond
	InputFrameBytes       = InputSampleRateHertz * ChannelCount * (BitsPerSample / 8) / 50

	InputAudioMIMEType  = "audio/pcm;rate=16000"
	OutputAudioMIMEType = "audio/pcm;rate=24000"

	defaultSetupTimeout       = 12 * time.Second
	defaultSessionTimeout     = 2 * time.Minute
	defaultSendTimeout        = 5 * time.Second
	defaultMaxInputBytes      = 4 << 20
	defaultMaxOutputBytes     = 8 << 20
	defaultMaxOutputChunk     = 1 << 20
	defaultMaxPendingBytes    = 2 << 20
	defaultMaxPendingEvents   = 128
	defaultMaxTranscriptBytes = 64 << 10

	hardMaxSetupTimeout      = 45 * time.Second
	hardMaxSessionTimeout    = 10 * time.Minute
	hardMaxSendTimeout       = 30 * time.Second
	hardMaxInputBytes        = 16 << 20
	hardMaxOutputBytes       = 32 << 20
	hardMaxPendingBytes      = 8 << 20
	hardMaxPendingEvents     = 1_024
	hardMaxTranscriptBytes   = 1 << 20
	hardMaxSystemPromptBytes = 32 << 10
)

var (
	ErrClosed              = errors.New("native voice session is closed")
	ErrDeadline            = errors.New("native voice deadline exceeded")
	ErrProvider            = errors.New("native voice provider failure")
	ErrProtocol            = errors.New("native voice provider protocol violation")
	ErrActivityState       = errors.New("native voice activity state is invalid")
	ErrInputCaptionPending = errors.New("native voice final input caption is pending")
	ErrPCMFrameSize        = errors.New("native voice PCM must be one 20 ms frame")
	ErrInputLimit          = errors.New("native voice input limit exceeded")
	ErrOutputLimit         = errors.New("native voice output limit exceeded")
	ErrPendingLimit        = errors.New("native voice pending buffer limit exceeded")
	ErrTranscriptLimit     = errors.New("native voice transcript limit exceeded")
	ErrUnexpectedFeature   = errors.New("native voice provider returned a disabled feature")
)

// Config bounds one native-audio session. Zero-valued optional fields receive
// conservative defaults. Model and Location, when supplied, must name the
// reviewed GA native-audio route and global Vertex endpoint.
type Config struct {
	ProjectID    string
	Location     string
	Model        string
	VoiceName    string
	SystemPrompt string

	SetupTimeout   time.Duration
	SessionTimeout time.Duration
	SendTimeout    time.Duration

	MaxInputBytes       int
	MaxOutputBytes      int
	MaxOutputChunkBytes int
	MaxPendingBytes     int
	MaxPendingEvents    int
	MaxTranscriptBytes  int
}

// EventKind identifies sanitized events emitted by Session.Receive.
type EventKind string

const (
	EventAudioPCM      EventKind = "audio_pcm"
	EventInputCaption  EventKind = "input_caption"
	EventOutputCaption EventKind = "output_caption"
	EventTurnComplete  EventKind = "turn_complete"
	EventInterrupted   EventKind = "interrupted"
)

// Event contains caller-owned bytes. Call Clear as soon as PCM or caption data
// has been forwarded. Output audio and captions are not emitted until
// CommitOutput; input captions and interruption signals are emitted immediately.
type Event struct {
	Kind               EventKind
	Route              string
	PCM                []byte
	CaptionUTF8        []byte
	CaptionFinal       bool
	SampleRateHertz    int
	TurnCompleteReason string
}

// Clear best-effort zeroizes event payloads owned by the caller.
func (e *Event) Clear() {
	if e == nil {
		return
	}
	clear(e.PCM)
	clear(e.CaptionUTF8)
	e.PCM = nil
	e.CaptionUTF8 = nil
	e.CaptionFinal = false
	e.SampleRateHertz = 0
	e.TurnCompleteReason = ""
}

// Session represents exactly one user turn. It serializes every provider write
// and uses one provider reader; callers may use methods from different
// goroutines. CommitOutput remains fail-closed until a final input caption has
// been received, and the caller must additionally complete its deterministic
// risk and route checks first. Open a new Session for the next user turn.
type Session interface {
	StartActivity(context.Context) error
	SendPCM20ms(context.Context, []byte) error
	EndActivity(context.Context) error
	CommitOutput() error
	DiscardOutput()
	Receive(context.Context) (Event, error)
	Close() error
}

// Opener is the dependency injected into the HTTP/WebSocket boundary.
type Opener interface {
	Open(context.Context) (Session, error)
}

// ProviderSession is the mockable subset of genai.Session used here. Close must
// unblock an in-progress SendRealtimeInput and Receive. Provider errors are
// classified before crossing the package boundary because the SDK may include
// raw server content in error strings.
type ProviderSession interface {
	SendRealtimeInput(genai.LiveRealtimeInput) error
	Receive() (*genai.LiveServerMessage, error)
	Close() error
}

// Dialer makes connection setup mockable without exposing provider details in
// the higher-level Session interface.
type Dialer interface {
	Connect(context.Context, string, *genai.LiveConnectConfig) (ProviderSession, error)
}

// Service opens bounded native-audio sessions with an immutable configuration.
type Service struct {
	config Config
	dialer Dialer
}

type genaiDialer struct {
	live *genai.Live
}

func (d genaiDialer) Connect(
	ctx context.Context,
	model string,
	config *genai.LiveConnectConfig,
) (ProviderSession, error) {
	return d.live.Connect(ctx, model, config)
}

// New initializes a Vertex AI client using ADC and the global v1 Live endpoint.
func New(ctx context.Context, config Config) (*Service, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, safeContextError("initialize", err)
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  config.ProjectID,
		Location: config.Location,
		Backend:  genai.BackendVertexAI,
		HTTPOptions: genai.HTTPOptions{
			APIVersion: APIVersion,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize native voice provider: %w", ErrProvider)
	}
	return &Service{config: config, dialer: genaiDialer{live: client.Live}}, nil
}

// NewWithDialer builds a service around a test or alternate provider dialer.
// The same production configuration validation is always applied.
func NewWithDialer(config Config, dialer Dialer) (*Service, error) {
	if dialer == nil {
		return nil, errors.New("native voice dialer is required")
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	return &Service{config: normalized, dialer: dialer}, nil
}

func normalizeConfig(config Config) (Config, error) {
	config.ProjectID = strings.TrimSpace(config.ProjectID)
	config.Location = strings.TrimSpace(config.Location)
	config.Model = strings.TrimSpace(config.Model)
	config.VoiceName = strings.TrimSpace(config.VoiceName)
	config.SystemPrompt = strings.TrimSpace(config.SystemPrompt)

	if config.ProjectID == "" {
		return Config{}, errors.New("native voice project is required")
	}
	if config.Location == "" {
		config.Location = DefaultLocation
	}
	if config.Location != DefaultLocation {
		return Config{}, errors.New("native voice location must be global")
	}
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.Model != DefaultModel {
		return Config{}, errors.New("native voice model is not approved")
	}
	if config.VoiceName == "" {
		config.VoiceName = DefaultVoice
	}
	if config.SystemPrompt == "" {
		return Config{}, errors.New("native voice system prompt is required")
	}
	if !utf8.ValidString(config.SystemPrompt) || len(config.SystemPrompt) > hardMaxSystemPromptBytes {
		return Config{}, errors.New("native voice system prompt is invalid")
	}

	applyDurationDefault(&config.SetupTimeout, defaultSetupTimeout)
	applyDurationDefault(&config.SessionTimeout, defaultSessionTimeout)
	applyDurationDefault(&config.SendTimeout, defaultSendTimeout)
	applyIntDefault(&config.MaxInputBytes, defaultMaxInputBytes)
	applyIntDefault(&config.MaxOutputBytes, defaultMaxOutputBytes)
	applyIntDefault(&config.MaxOutputChunkBytes, defaultMaxOutputChunk)
	applyIntDefault(&config.MaxPendingBytes, defaultMaxPendingBytes)
	applyIntDefault(&config.MaxPendingEvents, defaultMaxPendingEvents)
	applyIntDefault(&config.MaxTranscriptBytes, defaultMaxTranscriptBytes)

	if config.SetupTimeout <= 0 || config.SetupTimeout > hardMaxSetupTimeout ||
		config.SessionTimeout <= 0 || config.SessionTimeout > hardMaxSessionTimeout ||
		config.SendTimeout <= 0 || config.SendTimeout > hardMaxSendTimeout {
		return Config{}, errors.New("native voice deadline configuration is invalid")
	}
	if config.MaxInputBytes < InputFrameBytes || config.MaxInputBytes > hardMaxInputBytes {
		return Config{}, errors.New("native voice input limit is invalid")
	}
	if config.MaxOutputBytes <= 0 || config.MaxOutputBytes > hardMaxOutputBytes ||
		config.MaxOutputChunkBytes <= 0 || config.MaxOutputChunkBytes > config.MaxOutputBytes {
		return Config{}, errors.New("native voice output limit is invalid")
	}
	if config.MaxPendingBytes < config.MaxOutputChunkBytes ||
		config.MaxPendingBytes > hardMaxPendingBytes ||
		config.MaxPendingEvents <= 0 || config.MaxPendingEvents > hardMaxPendingEvents {
		return Config{}, errors.New("native voice pending limit is invalid")
	}
	if config.MaxTranscriptBytes <= 0 || config.MaxTranscriptBytes > hardMaxTranscriptBytes ||
		config.MaxTranscriptBytes > config.MaxPendingBytes {
		return Config{}, errors.New("native voice transcript limit is invalid")
	}
	return config, nil
}

func applyDurationDefault(target *time.Duration, value time.Duration) {
	if *target == 0 {
		*target = value
	}
}

func applyIntDefault(target *int, value int) {
	if *target == 0 {
		*target = value
	}
}

func (s *Service) connectConfig() *genai.LiveConnectConfig {
	return &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{
			genai.ModalityAudio,
			genai.ModalityText,
		},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: s.config.SystemPrompt}},
		},
		SpeechConfig: &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: s.config.VoiceName,
				},
			},
		},
		InputAudioTranscription: &genai.AudioTranscriptionConfig{
			LanguageCodes: []string{"ja-JP"},
		},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{
			LanguageCodes: []string{"ja-JP"},
		},
		RealtimeInputConfig: &genai.RealtimeInputConfig{
			AutomaticActivityDetection: &genai.AutomaticActivityDetection{Disabled: true},
			ActivityHandling:           genai.ActivityHandlingStartOfActivityInterrupts,
			TurnCoverage:               genai.TurnCoverageTurnIncludesOnlyActivity,
		},
		EnableAffectiveDialog: boolPointer(false),
		Proactivity: &genai.ProactivityConfig{
			ProactiveAudio: boolPointer(false),
		},
		SafetySettings: defaultSafetySettings(),
		// Tools, SessionResumption, ContextWindowCompression and
		// StreamTranslationConfig intentionally remain nil.
	}
}

func defaultSafetySettings() []*genai.SafetySetting {
	categories := []genai.HarmCategory{
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
		genai.HarmCategoryDangerousContent,
	}
	settings := make([]*genai.SafetySetting, 0, len(categories))
	for _, category := range categories {
		settings = append(settings, &genai.SafetySetting{
			Category:  category,
			Method:    genai.HarmBlockMethodSeverity,
			Threshold: genai.HarmBlockThresholdBlockMediumAndAbove,
		})
	}
	return settings
}

func boolPointer(value bool) *bool {
	return &value
}

func safeContextError(operation string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("native voice %s: %w", operation, ErrDeadline)
	}
	return fmt.Errorf("native voice %s: %w", operation, ErrClosed)
}
