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
