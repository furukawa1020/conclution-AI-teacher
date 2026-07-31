package config

import (
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
	t.Setenv("KOTAE_MAX_VOICE_BYTES", "")
	t.Setenv("KOTAE_SPEECH_MODEL", "")
	t.Setenv("KOTAE_SPEECH_VOICE", "")
	t.Setenv("KOTAE_VERTEX_PRIORITY", "")
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
	if cfg.MaxVoiceBytes != 13*1024*1024 {
		t.Fatalf("max voice bytes = %d; want 13 MiB", cfg.MaxVoiceBytes)
	}
	if cfg.SpeechModel != "latest_long" {
		t.Fatalf("speech model = %q; want latest_long", cfg.SpeechModel)
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
		{name: "request timeout collides with write deadline", key: "KOTAE_REQUEST_TIMEOUT", value: "51s"},
		{name: "voice timeout leaves no speech reserve", key: "KOTAE_VOICE_TIMEOUT", value: "14s"},
		{name: "voice timeout collides with write deadline", key: "KOTAE_VOICE_TIMEOUT", value: "51s"},
		{name: "unavailable speech primary", key: "KOTAE_SPEECH_MODEL", value: "chirp_3"},
		{name: "legacy speech primary", key: "KOTAE_SPEECH_MODEL", value: "long"},
		{name: "unreviewed speech primary", key: "KOTAE_SPEECH_MODEL", value: "short"},
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
