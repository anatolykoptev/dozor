package engine

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// HasDocker returns true if Docker commands should be available.
func (c Config) HasDocker() bool {
	return c.ComposePath != ""
}

// HasRemote returns true if remote monitoring is configured.
func (c Config) HasRemote() bool {
	return c.RemoteHost != "" || c.RemoteURL != ""
}

// Config holds all dozor configuration.
type Config struct {
	Host        string
	SSHPort     int
	ComposePath string
	Services    []string
	Timeout     time.Duration
	MCPPort     string

	WebhookURL    string
	WatchInterval time.Duration

	CPUThreshold    float64
	MemoryThreshold float64
	ErrorThreshold  int
	RestartThreshold int
	LogLines        int

	RemoteHost     string
	RemoteURL      string
	RemoteServices []string

	SystemdServices  []string
	RequiredAuthVars []string
}

// IsLocal returns true if the host is a local machine.
func (c Config) IsLocal() bool {
	h := strings.ToLower(c.Host)
	return h == "local" || h == "localhost" || h == "127.0.0.1"
}

// Init loads config from environment variables.
func Init() Config {
	return Config{
		Host:             env("DOZOR_HOST", "local"),
		SSHPort:          envInt("DOZOR_SSH_PORT", 22),
		ComposePath:      env("DOZOR_COMPOSE_PATH", ""),
		Services:         envList("DOZOR_SERVICES", ""),
		Timeout:          envDuration("DOZOR_TIMEOUT", 30*time.Second),
		MCPPort:          env("DOZOR_MCP_PORT", "8765"),
		WebhookURL:       env("DOZOR_WEBHOOK_URL", ""),
		WatchInterval:    envDurationStr("DOZOR_WATCH_INTERVAL", 4*time.Hour),
		CPUThreshold:     envFloat("DOZOR_CPU_THRESHOLD", 90),
		MemoryThreshold:  envFloat("DOZOR_MEMORY_THRESHOLD", 90),
		ErrorThreshold:   envInt("DOZOR_ERROR_THRESHOLD", 5),
		RestartThreshold: envInt("DOZOR_RESTART_THRESHOLD", 1),
		LogLines:         envInt("DOZOR_LOG_LINES", 100),
		RemoteHost:       env("DOZOR_REMOTE_HOST", ""),
		RemoteURL:        env("DOZOR_REMOTE_URL", ""),
		RemoteServices:   envList("DOZOR_REMOTE_SERVICES", ""),
		SystemdServices:  envList("DOZOR_SYSTEMD_SERVICES", ""),
		RequiredAuthVars: envList("DOZOR_REQUIRED_AUTH_VARS", ""),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envList(key, def string) []string {
	v := env(key, def)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envDuration parses seconds from env.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}

// envDurationStr parses Go duration string or "4h" from env.
func envDurationStr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
