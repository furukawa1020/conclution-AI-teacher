package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
)

const (
	defaultPort           = "8080"
	defaultVertexLocation = "global"
	defaultFastModel      = "vertexai/gemini-3.6-flash"
	defaultPrecisionModel = "vertexai/gemini-3.1-pro-preview"
	defaultSpeechLocation = "asia-northeast1"
	defaultSpeechModel    = "chirp_3"
	fallbackSpeechModel   = "long"
	defaultSpeechVoice    = "ja-JP-Chirp3-HD-Kore"
)

type Config struct {
	AppEnv             string
	Port               string
	ProjectID          string
	AllowedAppIDs      []string
	VertexLocation     string
	FastModel          string
	PrecisionModel     string
	VertexPriority     bool
	SpeechLocation     string
	SpeechModel        string
	SpeechVoice        string
	StateKey           []byte
	RequestTimeout     time.Duration
	VoiceTimeout       time.Duration
	MaxRequestBytes    int64
	MaxVoiceBytes      int64
	RateLimits         guard.Limits
	VoiceRateLimits    guard.Limits
	VoiceAppRateLimits guard.Limits
	AllowInsecureDev   bool
}

func Load() (Config, error) {
	perMinute, err := envBoundedInt(
		"KOTAE_RATE_LIMIT_PER_MINUTE",
		guard.DefaultPerMinute,
		guard.MinPerMinute,
		guard.MaxPerMinute,
	)
	if err != nil {
		return Config{}, err
	}
	perDay, err := envBoundedInt(
		"KOTAE_RATE_LIMIT_PER_DAY",
		guard.DefaultPerDay,
		guard.MinPerDay,
		guard.MaxPerDay,
	)
	if err != nil {
		return Config{}, err
	}
	voicePerMinute, err := envBoundedInt(
		"KOTAE_VOICE_RATE_LIMIT_PER_MINUTE",
		12,
		guard.MinPerMinute,
		guard.MaxPerMinute,
	)
	if err != nil {
		return Config{}, err
	}
	voicePerDay, err := envBoundedInt(
		"KOTAE_VOICE_RATE_LIMIT_PER_DAY",
		120,
		guard.MinPerDay,
		guard.MaxPerDay,
	)
	if err != nil {
		return Config{}, err
	}
	voiceAppPerMinute, err := envBoundedInt(
		"KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE",
		guard.MaxPerMinute,
		guard.MinPerMinute,
		guard.MaxPerMinute,
	)
	if err != nil {
		return Config{}, err
	}
	voiceAppPerDay, err := envBoundedInt(
		"KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY",
		guard.MaxPerDay,
		guard.MinPerDay,
		guard.MaxPerDay,
	)
	if err != nil {
		return Config{}, err
	}

	stateKey, err := decodeStateKey(os.Getenv("KOTAE_STATE_KEY_BASE64"))
	if err != nil {
		return Config{}, err
	}
	vertexPriority, err := envStrictBool("KOTAE_VERTEX_PRIORITY", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		AppEnv:          envOr("KOTAE_ENV", "production"),
		Port:            envOr("PORT", defaultPort),
		ProjectID:       firstNonEmpty(os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("GCLOUD_PROJECT")),
		AllowedAppIDs:   csvValues(os.Getenv("KOTAE_ALLOWED_APP_IDS")),
		VertexLocation:  envOr("GOOGLE_CLOUD_LOCATION", defaultVertexLocation),
		FastModel:       envOr("KOTAE_FAST_MODEL", defaultFastModel),
		PrecisionModel:  envOr("KOTAE_PRECISION_MODEL", defaultPrecisionModel),
		VertexPriority:  vertexPriority,
		SpeechLocation:  envOr("KOTAE_SPEECH_LOCATION", defaultSpeechLocation),
		SpeechModel:     envOr("KOTAE_SPEECH_MODEL", defaultSpeechModel),
		SpeechVoice:     envOr("KOTAE_SPEECH_VOICE", defaultSpeechVoice),
		StateKey:        stateKey,
		RequestTimeout:  envDurationOr("KOTAE_REQUEST_TIMEOUT", 25*time.Second),
		VoiceTimeout:    envDurationOr("KOTAE_VOICE_TIMEOUT", 50*time.Second),
		MaxRequestBytes: envInt64Or("KOTAE_MAX_REQUEST_BYTES", 32*1024),
		MaxVoiceBytes:   envInt64Or("KOTAE_MAX_VOICE_BYTES", 13*1024*1024),
		RateLimits:      guard.Limits{PerMinute: perMinute, PerDay: perDay},
		VoiceRateLimits: guard.Limits{PerMinute: voicePerMinute, PerDay: voicePerDay},
		VoiceAppRateLimits: guard.Limits{
			PerMinute: voiceAppPerMinute,
			PerDay:    voiceAppPerDay,
		},
		AllowInsecureDev: envBool("KOTAE_ALLOW_INSECURE_DEV"),
	}

	if strings.TrimSpace(cfg.Port) == "" {
		return Config{}, errors.New("PORT must not be empty")
	}
	if cfg.AllowInsecureDev && cfg.AppEnv != "local" && cfg.AppEnv != "test" {
		return Config{}, errors.New("KOTAE_ALLOW_INSECURE_DEV is only allowed when KOTAE_ENV is local or test")
	}
	if !cfg.AllowInsecureDev && strings.TrimSpace(cfg.ProjectID) == "" {
		return Config{}, errors.New("GOOGLE_CLOUD_PROJECT is required")
	}
	if !cfg.AllowInsecureDev && len(cfg.AllowedAppIDs) == 0 {
		return Config{}, errors.New("KOTAE_ALLOWED_APP_IDS must contain at least one Firebase App ID")
	}
	if !cfg.AllowInsecureDev && len(cfg.StateKey) != 32 {
		return Config{}, errors.New("KOTAE_STATE_KEY_BASE64 must decode to exactly 32 bytes")
	}
	if cfg.VertexPriority && cfg.VertexLocation != defaultVertexLocation {
		return Config{}, fmt.Errorf(
			"KOTAE_VERTEX_PRIORITY requires GOOGLE_CLOUD_LOCATION=%s",
			defaultVertexLocation,
		)
	}
	if cfg.SpeechLocation != defaultSpeechLocation {
		return Config{}, fmt.Errorf("KOTAE_SPEECH_LOCATION must be %s", defaultSpeechLocation)
	}
	if cfg.SpeechModel != defaultSpeechModel &&
		cfg.SpeechModel != fallbackSpeechModel {
		return Config{}, fmt.Errorf(
			"KOTAE_SPEECH_MODEL must be %s or %s",
			defaultSpeechModel,
			fallbackSpeechModel,
		)
	}
	if cfg.SpeechVoice != defaultSpeechVoice {
		return Config{}, fmt.Errorf(
			"KOTAE_SPEECH_VOICE must be %s",
			defaultSpeechVoice,
		)
	}
	if cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 50*time.Second {
		return Config{}, fmt.Errorf("KOTAE_REQUEST_TIMEOUT must be between 1s and 50s")
	}
	if cfg.VoiceTimeout < 5*time.Second || cfg.VoiceTimeout > 50*time.Second {
		return Config{}, fmt.Errorf("KOTAE_VOICE_TIMEOUT must be between 5s and 50s")
	}
	if cfg.MaxRequestBytes < 1024 || cfg.MaxRequestBytes > 1024*1024 {
		return Config{}, fmt.Errorf("KOTAE_MAX_REQUEST_BYTES must be between 1 KiB and 1 MiB")
	}
	if cfg.MaxVoiceBytes < 256*1024 || cfg.MaxVoiceBytes > 13*1024*1024 {
		return Config{}, fmt.Errorf("KOTAE_MAX_VOICE_BYTES must be between 256 KiB and 13 MiB")
	}
	if err := cfg.RateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid rate limits: %w", err)
	}
	if err := cfg.VoiceRateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid voice rate limits: %w", err)
	}
	if err := cfg.VoiceAppRateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid voice app rate limits: %w", err)
	}

	return cfg, nil
}

func decodeStateKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("KOTAE_STATE_KEY_BASE64 must be standard base64 for exactly 32 bytes")
	}
	return decoded, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func envBool(key string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && value
}

func envStrictBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64Or(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBoundedInt(key string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return parsed, nil
}

func csvValues(raw string) []string {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}
