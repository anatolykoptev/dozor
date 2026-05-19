package quotas

import (
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/dozor/internal/llmcfg"
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
	// GeminiAPIKey is the single key used by the Gemini quota probe.
	// Populated from llmcfg.Config.GeminiKeys[0] (which canonicalises
	// DOZOR_GEMINI_API_KEYS CSV, YAML side-loader, and the legacy
	// GEMINI_API_KEY single-key env — in that order of precedence).
	GeminiAPIKey string
	Interval     time.Duration
}

// loadInterval parses QUOTAS_PROBE_INTERVAL or returns defaultInterval.
func loadInterval() time.Duration {
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
	return interval
}

// LoadConfig reads configuration from environment variables.
// The Gemini key is read via GEMINI_API_KEY for backward compatibility.
// Prefer LoadConfigFrom when an llmcfg.Config is available so all key
// sources (CSV env, YAML side-loader, single-key compat) are respected.
func LoadConfig() Config {
	// Use llmcfg.Resolve with no YAML path so we still get the GEMINI_API_KEY
	// backwards-compat resolution without depending on a YAML file.
	llmCfg, err := llmcfg.Resolve("")
	if err != nil {
		slog.Warn("quotas: llmcfg.Resolve failed, Gemini key may be missing", slog.Any("error", err))
	}
	return loadConfigWithLLM(llmCfg)
}

// LoadConfigFrom builds a Config using the pre-resolved llmcfg.Config.
// Use this when engine.Init() has already called llmcfg.Resolve to avoid
// a second resolve (and a second startup log line).
func LoadConfigFrom(llmCfg llmcfg.Config) Config {
	return loadConfigWithLLM(llmCfg)
}

// loadConfigWithLLM is the shared implementation for LoadConfig and LoadConfigFrom.
func loadConfigWithLLM(llmCfg llmcfg.Config) Config {
	geminiKey := ""
	if len(llmCfg.GeminiKeys) > 0 {
		geminiKey = llmCfg.GeminiKeys[0]
	}
	return Config{
		WebshareAPIKey:    os.Getenv("WEBSHARE_API_KEY"),
		GitHubPAT:         os.Getenv("GITHUB_PAT"),
		AnthropicAdminKey: os.Getenv("ANTHROPIC_ADMIN_KEY"),
		AnthropicOrgID:    os.Getenv("ANTHROPIC_ORG_ID"),
		GeminiAPIKey:      geminiKey,
		Interval:          loadInterval(),
	}
}

// Enabled returns true if at least one vendor key is configured.
func (c Config) Enabled() bool {
	return c.WebshareAPIKey != "" ||
		c.GitHubPAT != "" ||
		c.AnthropicAdminKey != "" ||
		c.GeminiAPIKey != ""
}
