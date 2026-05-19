// Package llmcfg provides the canonical resolved LLM configuration for dozor.
// It applies env > YAML > defaults precedence (12-factor compliant).
//
// Single call site: engine.Init() calls Resolve once at startup.
// All subsystems (provider, engine/llm_check, quotas) consume the returned Config.
package llmcfg

import (
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the canonical resolved LLM configuration.
// Fields populated by env > YAML > defaults precedence at Resolve time.
type Config struct {
	// Primary completion endpoint (DOZOR_LLM_URL, default "http://127.0.0.1:8787/v1").
	PrimaryURL string
	// Primary completion key (DOZOR_LLM_API_KEY).
	PrimaryKey string
	// Primary completion model (DOZOR_LLM_MODEL).
	PrimaryModel string

	// Fallback completion endpoint (DOZOR_LLM_FALLBACK_URL).
	FallbackURL string
	// Fallback completion key (DOZOR_LLM_FALLBACK_API_KEY).
	FallbackKey string
	// Fallback completion model (DOZOR_LLM_FALLBACK_MODEL).
	FallbackModel string

	// GeminiKeys for health-check probes (DOZOR_GEMINI_API_KEYS CSV, or YAML
	// side-loader gemini-api-key entries, or GEMINI_API_KEY single-key compat).
	GeminiKeys []string

	// CheckURL is the CLIProxyAPI health-check endpoint (DOZOR_LLM_CHECK_URL).
	CheckURL string
	// CheckKey is the CLIProxyAPI health-check key (DOZOR_LLM_CHECK_API_KEY,
	// or first entry from YAML api-keys when env is empty).
	CheckKey string
	// CheckModels is the model list for CLIProxyAPI probes (DOZOR_LLM_CHECK_MODELS CSV).
	CheckModels []string
}

// Resolve loads Config with env > YAML > defaults precedence.
// yamlPath is optional; pass "" to skip the YAML loader.
// Logs once at startup which sources provided which fields.
func Resolve(yamlPath string) (Config, error) {
	var c Config
	c.loadFromEnv()
	if yamlPath != "" {
		if err := c.fillFromYAML(yamlPath); err != nil {
			return c, err
		}
	}
	c.applyDefaults()
	c.logSources()
	return c, nil
}

// loadFromEnv fills Config fields from environment variables.
// Only populates a field if the env var is non-empty (preserves empty for YAML fill).
func (c *Config) loadFromEnv() {
	if v := os.Getenv("DOZOR_LLM_URL"); v != "" {
		c.PrimaryURL = v
	}
	if v := os.Getenv("DOZOR_LLM_API_KEY"); v != "" {
		c.PrimaryKey = v
	}
	if v := os.Getenv("DOZOR_LLM_MODEL"); v != "" {
		c.PrimaryModel = v
	}
	if v := os.Getenv("DOZOR_LLM_FALLBACK_URL"); v != "" {
		c.FallbackURL = v
	}
	if v := os.Getenv("DOZOR_LLM_FALLBACK_API_KEY"); v != "" {
		c.FallbackKey = v
	}
	if v := os.Getenv("DOZOR_LLM_FALLBACK_MODEL"); v != "" {
		c.FallbackModel = v
	}
	if v := os.Getenv("DOZOR_LLM_CHECK_URL"); v != "" {
		c.CheckURL = v
	}
	if v := os.Getenv("DOZOR_LLM_CHECK_API_KEY"); v != "" {
		c.CheckKey = v
	}
	if v := os.Getenv("DOZOR_LLM_CHECK_MODELS"); v != "" {
		c.CheckModels = splitCSV(v)
	}

	// DOZOR_GEMINI_API_KEYS CSV takes priority over GEMINI_API_KEY single-key.
	if v := os.Getenv("DOZOR_GEMINI_API_KEYS"); v != "" {
		c.GeminiKeys = splitCSV(v)
	} else if v := os.Getenv("GEMINI_API_KEY"); v != "" {
		// Backwards-compat: GEMINI_API_KEY (used by quotas) populates first entry.
		c.GeminiKeys = []string{v}
	}
}

// cliProxyConfig is a partial representation of CLIProxyAPI config.yaml.
// Only the fields relevant to dozor are parsed.
type cliProxyConfig struct {
	APIKeys      []string `yaml:"api-keys"` //nolint:gosec // Not a hardcoded credential
	GeminiAPIKey []struct {
		APIKey string `yaml:"api-key"` //nolint:gosec // Not a hardcoded credential
	} `yaml:"gemini-api-key"`
}

// fillFromYAML fills empty Config fields from CLIProxyAPI config.yaml.
// Only fills fields that are still empty after loadFromEnv (env > YAML).
func (c *Config) fillFromYAML(path string) error {
	path = expandHome(path)
	data, err := os.ReadFile(path) //nolint:gosec // path from operator config
	if err != nil {
		slog.Warn("llmcfg: failed to read YAML config", slog.String("path", path), slog.Any("error", err))
		return nil // non-fatal; degrade gracefully
	}
	var cfg cliProxyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Warn("llmcfg: failed to parse YAML config", slog.String("path", path), slog.Any("error", err))
		return nil // non-fatal
	}

	// Fill CheckKey from first api-keys entry if not already set by env.
	if c.CheckKey == "" && len(cfg.APIKeys) > 0 {
		c.CheckKey = cfg.APIKeys[0]
	}

	// Fill GeminiKeys from YAML if not already set by env.
	if len(c.GeminiKeys) == 0 {
		keys := make([]string, 0, len(cfg.GeminiAPIKey))
		for _, k := range cfg.GeminiAPIKey {
			if k.APIKey != "" {
				keys = append(keys, k.APIKey)
			}
		}
		if len(keys) > 0 {
			c.GeminiKeys = keys
		}
	}
	return nil
}

// applyDefaults fills remaining empty fields with built-in defaults.
//
// CheckURL inherits PrimaryURL when not set (backward compat: existing deployments
// use DOZOR_LLM_URL for both the primary provider and the CLIProxyAPI health-check).
// CheckKey inherits PrimaryKey when not set (same YAML side-loader behaviour as
// engine.Init() pre-PR6: LLMCheckAPIKey = DOZOR_LLM_API_KEY unless overridden).
func (c *Config) applyDefaults() {
	if c.PrimaryURL == "" {
		c.PrimaryURL = "http://127.0.0.1:8787/v1"
	}
	// CheckURL inherits PrimaryURL so existing setups with DOZOR_LLM_URL only
	// still reach the CLIProxyAPI health-check endpoint.
	if c.CheckURL == "" {
		c.CheckURL = c.PrimaryURL
	}
	// CheckKey inherits PrimaryKey so existing setups with DOZOR_LLM_API_KEY only
	// still authenticate CLIProxyAPI health-check requests.
	if c.CheckKey == "" {
		c.CheckKey = c.PrimaryKey
	}
}

// logSources emits a single slog.Info at startup showing which fields are set.
// Helps operators diagnose config at a glance.
func (c *Config) logSources() {
	slog.Info("llmcfg resolved",
		slog.Bool("primary.url_set", c.PrimaryURL != ""),
		slog.Bool("primary.key_set", c.PrimaryKey != ""),
		slog.Bool("primary.model_set", c.PrimaryModel != ""),
		slog.Bool("fallback.url_set", c.FallbackURL != ""),
		slog.Bool("fallback.key_set", c.FallbackKey != ""),
		slog.Bool("check.url_set", c.CheckURL != ""),
		slog.Bool("check.key_set", c.CheckKey != ""),
		slog.Int("check.models", len(c.CheckModels)),
		slog.Int("gemini.keys", len(c.GeminiKeys)),
	)
}

// splitCSV splits a comma-separated string into trimmed non-empty parts.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}
