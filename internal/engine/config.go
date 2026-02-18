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

// HasUserServices returns true if user-level systemd services are configured.
func (c Config) HasUserServices() bool {
	return len(c.UserServices) > 0 && c.UserServicesUser != ""
}

// UserServiceNames returns just the names of configured user services.
func (c Config) UserServiceNames() []string {
	names := make([]string, len(c.UserServices))
	for i, s := range c.UserServices {
		names[i] = s.Name
	}
	return names
}

// FindUserService returns the UserService by name, or nil if not found.
func (c Config) FindUserService(name string) *UserService {
	for _, s := range c.UserServices {
		if s.Name == name {
			return &s
		}
	}
	return nil
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

	CPUThreshold     float64
	MemoryThreshold  float64
	ErrorThreshold   int
	RestartThreshold int
	LogLines         int

	RemoteHost     string
	RemoteURL      string
	RemoteServices []string
	RemoteSSHPort  int

	SystemdServices  []string
	RequiredAuthVars []string

	DiskThreshold float64
	DiskCritical  float64

	UserServices     []UserService
	UserServicesUser string

	TrackedBinaries []TrackedBinaryConfig
	GitHubToken     string

	// Web Search
	BraveAPIKey          string
	BraveMaxResults      int
	BraveEnabled         bool
	DuckDuckGoMaxResults int
	DuckDuckGoEnabled    bool
	PerplexityAPIKey     string
	PerplexityMaxResults int
	PerplexityEnabled    bool

	// Remote MCP Servers
	MCPServers map[string]MCPServerConfig
}

// MCPServerConfig holds config for a remote MCP server.
type MCPServerConfig struct {
	URL   string
	Alias string
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
		RemoteSSHPort:    envInt("DOZOR_REMOTE_SSH_PORT", 22),
		SystemdServices:  envList("DOZOR_SYSTEMD_SERVICES", ""),
		RequiredAuthVars: envList("DOZOR_REQUIRED_AUTH_VARS", ""),
		DiskThreshold:    envFloat("DOZOR_DISK_THRESHOLD", 80),
		DiskCritical:     envFloat("DOZOR_DISK_CRITICAL", 95),
		UserServices:     parseUserServices(env("DOZOR_USER_SERVICES", "")),
		UserServicesUser: env("DOZOR_USER_SERVICES_USER", ""),
		TrackedBinaries:  parseTrackedBinaries(env("DOZOR_TRACKED_BINARIES", "")),
		GitHubToken:      env("DOZOR_GITHUB_TOKEN", ""),

		// Web Search
		BraveAPIKey:          env("DOZOR_BRAVE_API_KEY", ""),
		BraveMaxResults:      envInt("DOZOR_BRAVE_MAX_RESULTS", 5),
		BraveEnabled:         envBool("DOZOR_BRAVE_ENABLED", false),
		DuckDuckGoMaxResults: envInt("DOZOR_DDG_MAX_RESULTS", 5),
		DuckDuckGoEnabled:    envBool("DOZOR_DDG_ENABLED", true),
		PerplexityAPIKey:     env("DOZOR_PERPLEXITY_API_KEY", ""),
		PerplexityMaxResults: envInt("DOZOR_PERPLEXITY_MAX_RESULTS", 5),
		PerplexityEnabled:    envBool("DOZOR_PERPLEXITY_ENABLED", false),

		// Remote MCP Servers
		MCPServers: parseMCPServers(env("DOZOR_MCP_SERVERS", "")),
	}
}

// parseTrackedBinaries parses "owner/repo:binary,owner/repo:binary" format.
func parseTrackedBinaries(raw string) []TrackedBinaryConfig {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	binaries := make([]TrackedBinaryConfig, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Split "owner/repo:binary" or "owner/repo" (binary = repo)
		var ownerRepo, binary string
		if idx := strings.Index(p, ":"); idx > 0 {
			ownerRepo = p[:idx]
			binary = p[idx+1:]
		} else {
			ownerRepo = p
		}
		slashIdx := strings.Index(ownerRepo, "/")
		if slashIdx <= 0 || slashIdx == len(ownerRepo)-1 {
			continue
		}
		owner := ownerRepo[:slashIdx]
		repo := ownerRepo[slashIdx+1:]
		if binary == "" {
			binary = repo
		}
		// Validate all parts
		if ok, _ := ValidateGitHubName(owner); !ok {
			continue
		}
		if ok, _ := ValidateGitHubName(repo); !ok {
			continue
		}
		if ok, _ := ValidateBinaryName(binary); !ok {
			continue
		}
		binaries = append(binaries, TrackedBinaryConfig{
			Owner:  owner,
			Repo:   repo,
			Binary: binary,
		})
	}
	return binaries
}

// parseUserServices parses "name:port,name:port" format.
func parseUserServices(raw string) []UserService {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	services := make([]UserService, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		svc := UserService{}
		if idx := strings.LastIndex(p, ":"); idx > 0 {
			svc.Name = strings.TrimSpace(p[:idx])
			if port, err := strconv.Atoi(strings.TrimSpace(p[idx+1:])); err == nil {
				svc.Port = port
			} else {
				svc.Name = p
			}
		} else {
			svc.Name = p
		}
		if svc.Name != "" {
			services = append(services, svc)
		}
	}
	return services
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

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return strings.ToLower(v) == "true" || v == "1"
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

// parseMCPServers parses "id=url,id=url" format.
func parseMCPServers(raw string) map[string]MCPServerConfig {
	if raw == "" {
		return nil
	}
	servers := make(map[string]MCPServerConfig)
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.Index(p, "=")
		if idx <= 0 {
			continue
		}
		id := strings.TrimSpace(p[:idx])
		url := strings.TrimSpace(p[idx+1:])
		if id != "" && url != "" {
			servers[id] = MCPServerConfig{URL: url, Alias: id}
		}
	}
	return servers
}
