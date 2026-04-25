package quotas

import (
	"log/slog"
	"os"
	"time"
)

const (
	// defaultInterval is the default probe interval.
	defaultInterval = 5 * time.Minute
)

// Config holds API credentials for each vendor probe and lifecycle settings.
// All key fields are optional — empty string means the vendor is disabled.
type Config struct {
	WebshareAPIKey    string
	GitHubPAT         string
	AnthropicAdminKey string
	AnthropicOrgID    string
	GeminiAPIKey      string
	Interval          time.Duration
}

// LoadConfig reads configuration from environment variables.
func LoadConfig() Config {
	interval := defaultInterval
	if raw := os.Getenv("QUOTAS_PROBE_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			interval = d
		} else {
			slog.Warn("quotas: invalid QUOTAS_PROBE_INTERVAL, using default",
				slog.String("raw", raw),
				slog.Any("err", err),
				slog.Duration("default", interval))
		}
	}
	return Config{
		WebshareAPIKey:    os.Getenv("WEBSHARE_API_KEY"),
		GitHubPAT:         os.Getenv("GITHUB_PAT"),
		AnthropicAdminKey: os.Getenv("ANTHROPIC_ADMIN_KEY"),
		AnthropicOrgID:    os.Getenv("ANTHROPIC_ORG_ID"),
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		Interval:          interval,
	}
}

// Enabled returns true if at least one vendor key is configured.
func (c Config) Enabled() bool {
	return c.WebshareAPIKey != "" ||
		c.GitHubPAT != "" ||
		c.AnthropicAdminKey != "" ||
		c.GeminiAPIKey != ""
}
