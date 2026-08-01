package config

import (
	"os"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
)

func setTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("KOTAE_ENV", "test")
	t.Setenv("KOTAE_ALLOW_INSECURE_DEV", "true")
	t.Setenv("KOTAE_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_VOICE_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_VOICE_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY", "")
	unsetTestEnvironment(t, "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE")
	unsetTestEnvironment(t, "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY")
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE", "")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", "")
	t.Setenv("KOTAE_PASSKEY_RP_ID", "")
	t.Setenv("KOTAE_PASSKEY_ORIGIN", "")
	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "")
	t.Setenv("KOTAE_MAX_VOICE_BYTES", "")
	t.Setenv("KOTAE_SPEECH_MODEL", "")
	t.Setenv("KOTAE_SPEECH_VOICE", "")
	t.Setenv("KOTAE_VERTEX_PRIORITY", "")
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "")
	t.Setenv("KOTAE_STATE_V2_WRITES", "")
}

func unsetTestEnvironment(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			if err := os.Setenv(key, value); err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Errorf("unset %s: %v", key, err)
		}
	})
}

func TestLoadUsesConservativeRateLimitDefaults(t *testing.T) {
	setTestEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimits.PerMinute != guard.DefaultPerMinute {
		t.Fatalf("per-minute limit = %d; want %d", cfg.RateLimits.PerMinute, guard.DefaultPerMinute)
	}
	if cfg.RateLimits.PerDay != guard.DefaultPerDay {
		t.Fatalf("daily limit = %d; want %d", cfg.RateLimits.PerDay, guard.DefaultPerDay)
	}
	if cfg.VoiceAppRateLimits != (guard.Limits{
		PerMinute: guard.MaxPerMinute,
		PerDay:    guard.MaxPerDay,
	}) {
		t.Fatalf("voice app rate limits = %+v", cfg.VoiceAppRateLimits)
	}
	if cfg.PasskeyClientRateLimits != (guard.Limits{PerMinute: 10, PerDay: 100}) {
		t.Fatalf("passkey client rate limits = %+v", cfg.PasskeyClientRateLimits)
	}
	if cfg.PasskeyAppCircuitBreaker != (guard.Limits{
		PerMinute: 300,
		PerDay:    20_000,
	}) {
		t.Fatalf("passkey app circuit breaker = %+v", cfg.PasskeyAppCircuitBreaker)
	}
	if cfg.PasskeyRPID != "kotae-ai.web.app" || cfg.PasskeyOrigin != "https://kotae-ai.web.app" {
		t.Fatalf("passkey RP = %q / %q", cfg.PasskeyRPID, cfg.PasskeyOrigin)
	}
	if cfg.RequireRecentPasskeyForVoice {
		t.Fatal("insecure development must default the recent-passkey voice gate off")
	}
	if cfg.MaxVoiceBytes != 13*1024*1024 {
		t.Fatalf("max voice bytes = %d; want 13 MiB", cfg.MaxVoiceBytes)
	}
	if cfg.SpeechModel != "long" {
		t.Fatalf("speech model = %q; want long", cfg.SpeechModel)
	}
	if cfg.SpeechVoice != "ja-JP-Chirp3-HD-Kore" {
		t.Fatalf(
			"speech voice = %q; want ja-JP-Chirp3-HD-Kore",
			cfg.SpeechVoice,
		)
	}
	if cfg.VertexPriority {
		t.Fatal("Vertex priority must remain opt-in")
	}
	if cfg.CoachRestatementBinding {
		t.Fatal("restatement tag issuance must remain opt-in for staged rollout")
	}
	if cfg.StateV2Writes {
		t.Fatal("extended state writes must remain opt-in for staged rollout")
	}
}

func TestLoadParsesTwoTierPasskeyLimits(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE", "7")
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY", "70")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE", "250")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", "15000")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PasskeyClientRateLimits != (guard.Limits{PerMinute: 7, PerDay: 70}) {
		t.Fatalf("passkey client limits = %+v", cfg.PasskeyClientRateLimits)
	}
	if cfg.PasskeyAppCircuitBreaker != (guard.Limits{PerMinute: 250, PerDay: 15_000}) {
		t.Fatalf("passkey app circuit breaker = %+v", cfg.PasskeyAppCircuitBreaker)
	}
}

func TestLoadRejectsLegacyPasskeyLimitEnvironment(t *testing.T) {
	for _, key := range []string{
		"KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE",
		"KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY",
	} {
		t.Run(key, func(t *testing.T) {
			setTestEnvironment(t)
			// Presence itself is rejected, including a deceptively empty value.
			t.Setenv(key, "")

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), key) ||
				!strings.Contains(err.Error(), "no longer supported") {
				t.Fatalf("legacy migration error = %v", err)
			}
		})
	}
}

func TestLoadParsesRecentPasskeyVoiceGateStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireRecentPasskeyForVoice {
		t.Fatal("recent passkey voice gate was not enabled")
	}
	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "later")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE") {
		t.Fatalf("malformed gate error = %v", err)
	}
}

func TestLoadRequiresRecentPasskeyByDefaultOutsideInsecureDev(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_ENV", "production")
	t.Setenv("KOTAE_ALLOW_INSECURE_DEV", "false")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "kotae-test")
	t.Setenv("KOTAE_ALLOWED_APP_IDS", "1:123:web:abc")
	t.Setenv("KOTAE_STATE_KEY_BASE64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireRecentPasskeyForVoice {
		t.Fatal("production must require a recent passkey when the gate is unspecified")
	}

	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequireRecentPasskeyForVoice {
		t.Fatal("an explicit false must remain available for a controlled migration")
	}
}

func TestLoadRequiresOneExactPasskeyOrigin(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_PASSKEY_RP_ID", "localhost")
	t.Setenv("KOTAE_PASSKEY_ORIGIN", "http://localhost:3000")
	if _, err := Load(); err != nil {
		t.Fatalf("local exact origin rejected: %v", err)
	}
	t.Setenv("KOTAE_PASSKEY_ORIGIN", "http://attacker.invalid")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "KOTAE_PASSKEY_RP_ID") {
		t.Fatalf("mismatched RP error = %v", err)
	}
}

func TestLoadParsesStateV2WritesStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.StateV2Writes {
		t.Fatal("state v2 writes were not enabled")
	}

	t.Setenv("KOTAE_STATE_V2_WRITES", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_STATE_V2_WRITES") {
		t.Fatalf("malformed state v2 writes error = %v", err)
	}
}

func TestLoadParsesCoachRestatementBindingStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CoachRestatementBinding {
		t.Fatal("restatement binding was not enabled")
	}

	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_COACH_RESTATEMENT_BINDING") {
		t.Fatalf("malformed restatement binding error = %v", err)
	}
}

func TestLoadAcceptsPriorityOnlyOnTheGlobalVertexEndpoint(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_VERTEX_PRIORITY", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VertexPriority {
		t.Fatal("Vertex priority was not enabled")
	}

	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-northeast1")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_VERTEX_PRIORITY") {
		t.Fatalf("regional priority error = %v", err)
	}
}

func TestLoadRejectsMalformedPriorityFlag(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_VERTEX_PRIORITY", "sometimes")

	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_VERTEX_PRIORITY") {
		t.Fatalf("malformed priority error = %v", err)
	}
}

func TestLoadAcceptsExplicitLongSpeechModel(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_SPEECH_MODEL", "long")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpeechModel != "long" {
		t.Fatalf("speech model = %q; want long", cfg.SpeechModel)
	}
}

func TestLoadAllowsThirteenMiBVoiceEnvelopeButNoMore(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_MAX_VOICE_BYTES", "13631488")
	if _, err := Load(); err != nil {
		t.Fatalf("13 MiB envelope rejected: %v", err)
	}

	t.Setenv("KOTAE_MAX_VOICE_BYTES", "13631489")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_MAX_VOICE_BYTES") {
		t.Fatalf("oversized envelope error = %v", err)
	}
}

func TestLoadAcceptsRateLimitsInsideSafeBounds(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_RATE_LIMIT_PER_MINUTE", "10")
	t.Setenv("KOTAE_RATE_LIMIT_PER_DAY", "100")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimits != (guard.Limits{PerMinute: 10, PerDay: 100}) {
		t.Fatalf("rate limits = %+v", cfg.RateLimits)
	}
}

func TestLoadRejectsUnsafeRateLimitOverrides(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "minute malformed", key: "KOTAE_RATE_LIMIT_PER_MINUTE", value: "many"},
		{name: "minute disabled", key: "KOTAE_RATE_LIMIT_PER_MINUTE", value: "0"},
		{name: "minute too high", key: "KOTAE_RATE_LIMIT_PER_MINUTE", value: "21"},
		{name: "day malformed", key: "KOTAE_RATE_LIMIT_PER_DAY", value: "many"},
		{name: "day disabled", key: "KOTAE_RATE_LIMIT_PER_DAY", value: "0"},
		{name: "day too high", key: "KOTAE_RATE_LIMIT_PER_DAY", value: "201"},
		{name: "voice app minute too high", key: "KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE", value: "21"},
		{name: "voice app day disabled", key: "KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY", value: "0"},
		{name: "passkey client minute too high", key: "KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE", value: "21"},
		{name: "passkey client day too high", key: "KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY", value: "201"},
		{name: "passkey app breaker minute too high", key: "KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE", value: "301"},
		{name: "passkey app breaker day too high", key: "KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", value: "20001"},
		{name: "passkey app breaker disabled", key: "KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", value: "0"},
		{name: "request timeout collides with write deadline", key: "KOTAE_REQUEST_TIMEOUT", value: "51s"},
		{name: "voice timeout leaves no speech reserve", key: "KOTAE_VOICE_TIMEOUT", value: "14s"},
		{name: "voice timeout collides with write deadline", key: "KOTAE_VOICE_TIMEOUT", value: "51s"},
		{name: "single utterance speech primary", key: "KOTAE_SPEECH_MODEL", value: "short"},
		{name: "unavailable speech primary", key: "KOTAE_SPEECH_MODEL", value: "chirp_3"},
		{name: "unreviewed alias speech primary", key: "KOTAE_SPEECH_MODEL", value: "latest_long"},
		{name: "unreviewed speech voice", key: "KOTAE_SPEECH_VOICE", value: "ja-JP-Neural2-B"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setTestEnvironment(t)
			t.Setenv(test.key, test.value)

			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded; want error")
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Fatalf("error = %q; want environment variable name", err)
			}
		})
	}
}
