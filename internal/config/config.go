package config

import (
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
)

type Config struct {
	AppEnv           string
	Port             string
	ProjectID        string
	AllowedAppIDs    []string
	VertexLocation   string
	FastModel        string
	RequestTimeout   time.Duration
	MaxRequestBytes  int64
	RateLimits       guard.Limits
	AllowInsecureDev bool
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

	cfg := Config{
		AppEnv:           envOr("KOTAE_ENV", "production"),
		Port:             envOr("PORT", defaultPort),
		ProjectID:        firstNonEmpty(os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("GCLOUD_PROJECT")),
		AllowedAppIDs:    csvValues(os.Getenv("KOTAE_ALLOWED_APP_IDS")),
		VertexLocation:   envOr("GOOGLE_CLOUD_LOCATION", defaultVertexLocation),
		FastModel:        envOr("KOTAE_FAST_MODEL", defaultFastModel),
		RequestTimeout:   envDurationOr("KOTAE_REQUEST_TIMEOUT", 25*time.Second),
		MaxRequestBytes:  envInt64Or("KOTAE_MAX_REQUEST_BYTES", 32*1024),
		RateLimits:       guard.Limits{PerMinute: perMinute, PerDay: perDay},
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
	if cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 55*time.Second {
		return Config{}, fmt.Errorf("KOTAE_REQUEST_TIMEOUT must be between 1s and 55s")
	}
	if cfg.MaxRequestBytes < 1024 || cfg.MaxRequestBytes > 1024*1024 {
		return Config{}, fmt.Errorf("KOTAE_MAX_REQUEST_BYTES must be between 1 KiB and 1 MiB")
	}
	if err := cfg.RateLimits.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid rate limits: %w", err)
	}

	return cfg, nil
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
