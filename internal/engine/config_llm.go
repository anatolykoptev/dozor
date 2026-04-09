package engine

import (
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// cliProxyConfig is a partial representation of CLIProxyAPI config.yaml.
type cliProxyConfig struct {
	APIKeys      []string `yaml:"api-keys"` //nolint:gosec // Not a hardcoded credential
	GeminiAPIKey []struct {
		APIKey string `yaml:"api-key"` //nolint:gosec // Not a hardcoded credential
	} `yaml:"gemini-api-key"`
}

// parsedProxyConfig holds values extracted from CLIProxyAPI config.yaml.
type parsedProxyConfig struct {
	ProxyAPIKey   string   // first entry from api-keys
	GeminiAPIKeys []string // all gemini-api-key entries
}

// parseLLMConfig reads CLIProxyAPI config.yaml and extracts proxy API key + Gemini keys.
func parseLLMConfig(path string) parsedProxyConfig {
	var result parsedProxyConfig
	if path == "" {
		return result
	}
	path = expandHome(path)
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("failed to read LLM config", slog.String("path", path), slog.Any("error", err))
		return result
	}
	var cfg cliProxyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse LLM config", slog.String("path", path), slog.Any("error", err))
		return result
	}
	if len(cfg.APIKeys) > 0 {
		result.ProxyAPIKey = cfg.APIKeys[0]
	}
	result.GeminiAPIKeys = make([]string, 0, len(cfg.GeminiAPIKey))
	for _, k := range cfg.GeminiAPIKey {
		if k.APIKey != "" {
			result.GeminiAPIKeys = append(result.GeminiAPIKeys, k.APIKey)
		}
	}
	slog.Info("loaded LLM config",
		slog.Int("gemini_keys", len(result.GeminiAPIKeys)),
		slog.Bool("proxy_key", result.ProxyAPIKey != ""),
		slog.String("path", path))
	return result
}

// defaultSuppressWarnings are the built-in benign service warnings suppressed by default.
// These can be overridden entirely by setting DOZOR_SUPPRESS_WARNINGS.
var defaultSuppressWarnings = map[string]string{
	"qdrant":    "telemetry errors (benign)",
	"searxng":   "rate limits (self-heals)",
	"go-hully":  "pool exhaustion (resets hourly)",
	"go-social": "deploy restarts (benign)",
}

// parseSuppressWarnings parses "service:reason,service:reason" format.
// Returns defaultSuppressWarnings when raw is empty.
func parseSuppressWarnings(raw string) map[string]string {
	if raw == "" {
		// Copy the default map so callers cannot mutate the package-level var.
		out := make(map[string]string, len(defaultSuppressWarnings))
		for k, v := range defaultSuppressWarnings {
			out[k] = v
		}
		return out
	}
	parts := strings.Split(raw, ",")
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.Index(p, ":")
		if idx <= 0 {
			continue
		}
		svc := strings.TrimSpace(p[:idx])
		reason := strings.TrimSpace(p[idx+1:])
		if svc != "" {
			out[svc] = reason
		}
	}
	return out
}

// defaultInternalPorts are the built-in ports that should not be exposed to 0.0.0.0.
var defaultInternalPorts = map[string]string{
	"5432":  "PostgreSQL",
	"3306":  "MySQL",
	"6379":  "Redis",
	"27017": "MongoDB",
	"9200":  "Elasticsearch",
	"2379":  "etcd",
	"5672":  "RabbitMQ",
	"15672": "RabbitMQ Management",
	"6333":  "Qdrant",
}

// parseInternalPorts parses "port:name,port:name" format.
// Returns defaultInternalPorts when raw is empty.
func parseInternalPorts(raw string) map[string]string {
	if raw == "" {
		out := make(map[string]string, len(defaultInternalPorts))
		for k, v := range defaultInternalPorts {
			out[k] = v
		}
		return out
	}
	parts := strings.Split(raw, ",")
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.Index(p, ":")
		if idx <= 0 {
			continue
		}
		port := strings.TrimSpace(p[:idx])
		name := strings.TrimSpace(p[idx+1:])
		if port != "" {
			out[port] = name
		}
	}
	return out
}

// defaultRootAllowed are container name prefixes allowed to run as root by default.
var defaultRootAllowed = []string{"postgres", "redis", "traefik", "caddy"}

// parseRootAllowed parses "name,name" format into a lookup map.
// Returns defaults when raw is empty.
func parseRootAllowed(raw string) map[string]bool {
	if raw == "" {
		out := make(map[string]bool, len(defaultRootAllowed))
		for _, v := range defaultRootAllowed {
			out[v] = true
		}
		return out
	}
	parts := strings.Split(raw, ",")
	out := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// defaultDangerousMounts are host paths that must not be mounted into containers by default.
var defaultDangerousMounts = []string{
	"/.claude", "/.ssh", "/.aws", "/.kube", "/.gnupg",
	"/etc/shadow", "/etc/passwd", "/var/run/docker.sock",
}

// parseDangerousMounts parses "path,path" format.
// Returns defaults when raw is empty.
func parseDangerousMounts(raw string) []string {
	if raw == "" {
		out := make([]string, len(defaultDangerousMounts))
		copy(out, defaultDangerousMounts)
		return out
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
