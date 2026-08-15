package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
)

const (
	defaultPort                   = "8080"
	defaultVertexLocation         = "global"
	defaultFastModel              = "vertexai/gemini-3.6-flash"
	defaultPrecisionModel         = "vertexai/gemini-3.1-pro-preview"
	defaultSpeechLocation         = "asia-northeast1"
	defaultSpeechModel            = "chirp_3"
	defaultSpeechVoice            = "ja-JP-Chirp3-HD-Kore"
	defaultNativeAudioLocation    = "us-central1"
	defaultNativeAudioModel       = "gemini-live-2.5-flash-native-audio"
	defaultNativeAudioVoice       = "Kore"
	defaultPasskeyRPID            = "kotae-ai.web.app"
	defaultPasskeyOrigin          = "https://kotae-ai.web.app"
	defaultPasskeyClientPerMinute = 10
	defaultPasskeyClientPerDay    = 100
	minVoiceTimeout               = 15 * time.Second
)

type Config struct {
	AppEnv                       string
	Port                         string
	ProjectID                    string
	AllowedAppIDs                []string
	VertexLocation               string
	FastModel                    string
	PrecisionModel               string
	VertexPriority               bool
	CoachRestatementBinding      bool
	StateV2Writes                bool
	AnswerProofWrites            bool
	VerifierProgressWrites       bool
	RetrievalPolicyEnabled       bool
	AnswerTransitionWrites       bool
	AnswerTransitionEnabled      bool
	SpeechLocation               string
	SpeechModel                  string
	SpeechVoice                  string
	NativeAudioEnabled           bool
	NativeCaptionHandoffEnabled  bool
	NativeAudioLocation          string
	NativeAudioModel             string
	NativeAudioVoice             string
	StateKey                     []byte
	RequestTimeout               time.Duration
	VoiceTimeout                 time.Duration
	MaxRequestBytes              int64
	MaxVoiceBytes                int64
	RateLimits                   guard.Limits
	VoiceRateLimits              guard.Limits
	VoiceAppRateLimits           guard.Limits
	GuestVoiceRateLimits         guard.Limits
	GuestVoiceAppRateLimits      guard.Limits
	PasskeyClientRateLimits      guard.Limits
	PasskeyAppCircuitBreaker     guard.Limits
	PasskeyRPID                  string
	PasskeyOrigin                string
	RequireRecentPasskeyForVoice bool
	GuestModeEnabled             bool
	AllowInsecureDev             bool
}

func Load() (Config, error) {
	if err := rejectLegacyEnvironment(); err != nil {
		return Config{}, err
	}
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
	guestVoicePerMinute, err := envBoundedInt("KOTAE_GUEST_VOICE_RATE_LIMIT_PER_MINUTE", 4, guard.MinPerMinute, guard.MaxPerMinute)
	if err != nil {
		return Config{}, err
	}
	guestVoicePerDay, err := envBoundedInt("KOTAE_GUEST_VOICE_RATE_LIMIT_PER_DAY", 16, guard.MinPerDay, guard.MaxPerDay)
	if err != nil {
		return Config{}, err
	}
	guestVoiceAppPerMinute, err := envBoundedInt("KOTAE_GUEST_VOICE_APP_RATE_LIMIT_PER_MINUTE", 20, guard.MinPerMinute, guard.MaxPerMinute)
	if err != nil {
		return Config{}, err
	}
	guestVoiceAppPerDay, err := envBoundedInt("KOTAE_GUEST_VOICE_APP_RATE_LIMIT_PER_DAY", 200, guard.MinPerDay, guard.MaxPerDay)
	if err != nil {
		return Config{}, err
	}
	passkeyClientPerMinute, err := envBoundedInt(
		"KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE",
		defaultPasskeyClientPerMinute,
		guard.MinPerMinute,
		guard.MaxPerMinute,
	)
	if err != nil {
		return Config{}, err
	}
	passkeyClientPerDay, err := envBoundedInt(
		"KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY",
		defaultPasskeyClientPerDay,
		guard.MinPerDay,
		guard.MaxPerDay,
	)
	if err != nil {
		return Config{}, err
	}
	passkeyAppPerMinute, err := envBoundedInt(
		"KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE",
		guard.MaxPasskeyAppCircuitBreakerPerMinute,
		guard.MinPerMinute,
		guard.MaxPasskeyAppCircuitBreakerPerMinute,
	)
	if err != nil {
		return Config{}, err
	}
	passkeyAppPerDay, err := envBoundedInt(
		"KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY",
		guard.MaxPasskeyAppCircuitBreakerPerDay,
		guard.MinPerDay,
		guard.MaxPasskeyAppCircuitBreakerPerDay,
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
	coachRestatementBinding, err := envStrictBool(
		"KOTAE_COACH_RESTATEMENT_BINDING",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	stateV2Writes, err := envStrictBool("KOTAE_STATE_V2_WRITES", false)
	if err != nil {
		return Config{}, err
	}
	answerProofWrites, err := envStrictBool(
		"KOTAE_ANSWER_PROOF_WRITES",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	verifierProgressWrites, err := envStrictBool(
		"KOTAE_VERIFIER_PROGRESS_WRITES",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	retrievalPolicyEnabled, err := envStrictBool(
		"KOTAE_RETRIEVAL_POLICY_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	answerTransitionWrites, err := envStrictBool(
		"KOTAE_ANSWER_TRANSITION_WRITES",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	answerTransitionEnabled, err := envStrictBool(
		"KOTAE_ANSWER_TRANSITION_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	nativeAudioEnabled, err := envStrictBool(
		"KOTAE_NATIVE_AUDIO_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	nativeCaptionHandoffEnabled, err := envStrictBool(
		"KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	allowInsecureDev := envBool("KOTAE_ALLOW_INSECURE_DEV")
	requireRecentPasskey, err := envStrictBool(
		"KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE",
		!allowInsecureDev,
	)
	if err != nil {
		return Config{}, err
	}
	guestModeEnabled, err := envStrictBool("KOTAE_GUEST_MODE_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		AppEnv:                      envOr("KOTAE_ENV", "production"),
		Port:                        envOr("PORT", defaultPort),
		ProjectID:                   firstNonEmpty(os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("GCLOUD_PROJECT")),
		AllowedAppIDs:               csvValues(os.Getenv("KOTAE_ALLOWED_APP_IDS")),
		VertexLocation:              envOr("GOOGLE_CLOUD_LOCATION", defaultVertexLocation),
		FastModel:                   envOr("KOTAE_FAST_MODEL", defaultFastModel),
		PrecisionModel:              envOr("KOTAE_PRECISION_MODEL", defaultPrecisionModel),
		VertexPriority:              vertexPriority,
		CoachRestatementBinding:     coachRestatementBinding,
		StateV2Writes:               stateV2Writes,
		AnswerProofWrites:           answerProofWrites,
		VerifierProgressWrites:      verifierProgressWrites,
		RetrievalPolicyEnabled:      retrievalPolicyEnabled,
		AnswerTransitionWrites:      answerTransitionWrites,
		AnswerTransitionEnabled:     answerTransitionEnabled,
		SpeechLocation:              envOr("KOTAE_SPEECH_LOCATION", defaultSpeechLocation),
		SpeechModel:                 envOr("KOTAE_SPEECH_MODEL", defaultSpeechModel),
		SpeechVoice:                 envOr("KOTAE_SPEECH_VOICE", defaultSpeechVoice),
		NativeAudioEnabled:          nativeAudioEnabled,
		NativeCaptionHandoffEnabled: nativeCaptionHandoffEnabled,
		NativeAudioLocation:         envOr("KOTAE_NATIVE_AUDIO_LOCATION", defaultNativeAudioLocation),
		NativeAudioModel:            envOr("KOTAE_NATIVE_AUDIO_MODEL", defaultNativeAudioModel),
		NativeAudioVoice:            envOr("KOTAE_NATIVE_AUDIO_VOICE", defaultNativeAudioVoice),
		StateKey:                    stateKey,
		RequestTimeout:              envDurationOr("KOTAE_REQUEST_TIMEOUT", 25*time.Second),
		VoiceTimeout:                envDurationOr("KOTAE_VOICE_TIMEOUT", 50*time.Second),
		MaxRequestBytes:             envInt64Or("KOTAE_MAX_REQUEST_BYTES", 32*1024),
		MaxVoiceBytes:               envInt64Or("KOTAE_MAX_VOICE_BYTES", 13*1024*1024),
		RateLimits:                  guard.Limits{PerMinute: perMinute, PerDay: perDay},
		VoiceRateLimits:             guard.Limits{PerMinute: voicePerMinute, PerDay: voicePerDay},
		VoiceAppRateLimits: guard.Limits{
			PerMinute: voiceAppPerMinute,
			PerDay:    voiceAppPerDay,
		},
		GuestVoiceRateLimits: guard.Limits{PerMinute: guestVoicePerMinute, PerDay: guestVoicePerDay},
		GuestVoiceAppRateLimits: guard.Limits{
			PerMinute: guestVoiceAppPerMinute,
			PerDay:    guestVoiceAppPerDay,
		},
		PasskeyClientRateLimits: guard.Limits{
			PerMinute: passkeyClientPerMinute,
			PerDay:    passkeyClientPerDay,
		},
		PasskeyAppCircuitBreaker: guard.Limits{
			PerMinute: passkeyAppPerMinute,
			PerDay:    passkeyAppPerDay,
		},
		PasskeyRPID:                  envOr("KOTAE_PASSKEY_RP_ID", defaultPasskeyRPID),
		PasskeyOrigin:                envOr("KOTAE_PASSKEY_ORIGIN", defaultPasskeyOrigin),
		RequireRecentPasskeyForVoice: requireRecentPasskey,
		GuestModeEnabled:             guestModeEnabled,
		AllowInsecureDev:             allowInsecureDev,
	}

	if strings.TrimSpace(cfg.Port) == "" {
		return Config{}, errors.New("PORT must not be empty")
	}
	if cfg.AllowInsecureDev && cfg.AppEnv != "local" && cfg.AppEnv != "test" {
		return Config{}, errors.New("KOTAE_ALLOW_INSECURE_DEV is only allowed when KOTAE_ENV is local or test")
	}
	if cfg.RetrievalPolicyEnabled &&
		(!cfg.StateV2Writes || !cfg.AnswerProofWrites ||
			!cfg.CoachRestatementBinding) {
		return Config{}, errors.New(
			"KOTAE_RETRIEVAL_POLICY_ENABLED requires KOTAE_STATE_V2_WRITES, KOTAE_ANSWER_PROOF_WRITES, and KOTAE_COACH_RESTATEMENT_BINDING",
		)
	}
	if cfg.VerifierProgressWrites && !cfg.StateV2Writes {
		return Config{}, errors.New(
			"KOTAE_VERIFIER_PROGRESS_WRITES requires KOTAE_STATE_V2_WRITES",
		)
	}
	if cfg.AnswerTransitionWrites &&
		(!cfg.StateV2Writes || !cfg.AnswerProofWrites) {
		return Config{}, errors.New(
			"KOTAE_ANSWER_TRANSITION_WRITES requires KOTAE_STATE_V2_WRITES and KOTAE_ANSWER_PROOF_WRITES",
		)
	}
	if cfg.AnswerTransitionEnabled &&
		(!cfg.AnswerTransitionWrites || !cfg.RetrievalPolicyEnabled) {
		return Config{}, errors.New(
			"KOTAE_ANSWER_TRANSITION_ENABLED requires KOTAE_ANSWER_TRANSITION_WRITES and KOTAE_RETRIEVAL_POLICY_ENABLED",
		)
	}
	if cfg.NativeCaptionHandoffEnabled && !cfg.NativeAudioEnabled {
		return Config{}, errors.New(
			"KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED requires KOTAE_NATIVE_AUDIO_ENABLED",
		)
	}
	if cfg.NativeCaptionHandoffEnabled &&
		(!cfg.RetrievalPolicyEnabled || !cfg.VerifierProgressWrites) {
		return Config{}, errors.New(
			"KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED requires KOTAE_RETRIEVAL_POLICY_ENABLED and KOTAE_VERIFIER_PROGRESS_WRITES",
		)
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
	if cfg.SpeechModel != defaultSpeechModel {
		return Config{}, fmt.Errorf("KOTAE_SPEECH_MODEL must be %s", defaultSpeechModel)
	}
	if cfg.SpeechVoice != defaultSpeechVoice {
		return Config{}, fmt.Errorf(
			"KOTAE_SPEECH_VOICE must be %s",
			defaultSpeechVoice,
		)
	}
	if cfg.NativeAudioLocation != defaultNativeAudioLocation {
		return Config{}, fmt.Errorf(
			"KOTAE_NATIVE_AUDIO_LOCATION must be %s",
			defaultNativeAudioLocation,
		)
	}
	if cfg.NativeAudioModel != defaultNativeAudioModel {
		return Config{}, fmt.Errorf(
			"KOTAE_NATIVE_AUDIO_MODEL must be %s",
			defaultNativeAudioModel,
		)
	}
	if cfg.NativeAudioVoice != defaultNativeAudioVoice {
		return Config{}, fmt.Errorf(
			"KOTAE_NATIVE_AUDIO_VOICE must be %s",
			defaultNativeAudioVoice,
		)
	}
	if cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 50*time.Second {
		return Config{}, fmt.Errorf("KOTAE_REQUEST_TIMEOUT must be between 1s and 50s")
	}
	if cfg.VoiceTimeout < minVoiceTimeout || cfg.VoiceTimeout > 50*time.Second {
		return Config{}, fmt.Errorf(
			"KOTAE_VOICE_TIMEOUT must be between %s and 50s",
			minVoiceTimeout,
		)
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
	if err := cfg.GuestVoiceRateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid guest voice rate limits: %w", err)
	}
	if err := cfg.GuestVoiceAppRateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid guest voice app rate limits: %w", err)
	}
	if err := cfg.PasskeyClientRateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid passkey client rate limits: %w", err)
	}
	if err := cfg.PasskeyAppCircuitBreaker.ValidatePasskeyAppCircuitBreaker(); err != nil {
		return Config{}, fmt.Errorf("invalid passkey app circuit breaker: %w", err)
	}
	if err := validatePasskeyOrigin(cfg.PasskeyRPID, cfg.PasskeyOrigin, cfg.AllowInsecureDev); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func rejectLegacyEnvironment() error {
	legacy := []struct {
		old string
		new string
	}{
		{
			old: "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE",
			new: "KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE and KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE",
		},
		{
			old: "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY",
			new: "KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY and KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY",
		},
		{
			old: "KOTAE_RETRIEVAL_BELIEF_WRITES",
			new: "KOTAE_VERIFIER_PROGRESS_WRITES",
		},
	}
	for _, migration := range legacy {
		if _, set := os.LookupEnv(migration.old); set {
			return fmt.Errorf(
				"%s is no longer supported; unset it and configure %s",
				migration.old,
				migration.new,
			)
		}
	}
	return nil
}

func validatePasskeyOrigin(rpID, origin string, allowInsecureDev bool) error {
	rpID = strings.TrimSpace(rpID)
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("KOTAE_PASSKEY_ORIGIN must be one exact origin")
	}
	if parsed.Hostname() != rpID || strings.Contains(rpID, ":") || strings.Contains(rpID, "/") {
		return errors.New("KOTAE_PASSKEY_RP_ID must exactly match the passkey origin hostname")
	}
	if parsed.Scheme != "https" && !(allowInsecureDev && parsed.Scheme == "http") {
		return errors.New("KOTAE_PASSKEY_ORIGIN must use https")
	}
	if !allowInsecureDev && parsed.Port() != "" {
		return errors.New("KOTAE_PASSKEY_ORIGIN must not include a production port")
	}
	return nil
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
