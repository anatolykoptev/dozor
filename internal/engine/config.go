package engine

import (
	"strings"
	"time"
)

const (
	defaultSSHPort                = 1987
	sshStandardPort               = 22
	defaultTimeoutSec             = 30
	defaultErrorThreshold         = 5
	defaultRemoteCheckIntervalMin = 3
	defaultDiskThreshold          = 80
	defaultDiskCritical           = 95
	defaultBraveMaxResults        = 5
	defaultFlapHigh               = 0.7
	defaultFlapLow                = 0.3
	defaultCBKBThreshold          = 3
	defaultWatchIntervalHours     = 4
	defaultCPUThreshold           = 90
	defaultMemoryThreshold        = 90
	defaultCBLLMResetMin          = 10
	defaultCBKBResetMin           = 5
	defaultRestartThreshold       = 3
)

// Config holds all dozor configuration.
type Config struct {
	Host          string
	SSHPort       int
	ComposePath   string
	Services      []string
	Timeout       time.Duration
	MCPPort       string
	WebhookURL    string
	WatchInterval time.Duration

	CPUThreshold     float64
	MemoryThreshold  float64
	ErrorThreshold   int
	RestartThreshold int
	LogLines         int

	RemoteHost          string
	RemoteURL           string
	RemoteServices      []string
	RemoteSSHPort       int
	RemoteCheckInterval time.Duration
	SystemdServices     []string
	RequiredAuthVars    []string
	DiskThreshold       float64
	DiskCritical        float64
	UserServices        []UserService
	UserServicesUser    string
	TrackedBinaries     []TrackedBinaryConfig
	GitHubToken         string

	// Web Search
	BraveAPIKey          string
	BraveMaxResults      int
	BraveEnabled         bool
	DuckDuckGoMaxResults int
	DuckDuckGoEnabled    bool
	PerplexityAPIKey     string
	PerplexityMaxResults int
	PerplexityEnabled    bool

	MCPServers   map[string]MCPServerConfig
	KBServer     string
	KBUser       string
	KBCube       string
	KBSearchTool string
	KBSaveTool   string

	AlertConfirmCount int
	FlapWindow        int
	FlapHigh          float64
	FlapLow           float64

	CBKBThreshold  int
	CBKBReset      time.Duration
	CBLLMThreshold int
	CBLLMReset     time.Duration

	LLMConfigPath  string
	GeminiAPIKeys  []string
	LLMCheckURL    string
	LLMCheckAPIKey string
	LLMCheckModels []string

	SuppressWarnings      map[string]string
	InternalPorts         map[string]string
	RootAllowedContainers map[string]bool
	DangerousHostMounts   []string
}

// MCPServerConfig holds config for a remote MCP server.
type MCPServerConfig struct {
	URL   string
	Alias string
}

// HasRemote returns true if remote monitoring is configured.
func (c Config) HasRemote() bool {
	return c.RemoteHost != "" || c.RemoteURL != ""
}

// HasUserServices returns true if user-level systemd services are configured via env.
func (c Config) HasUserServices() bool {
	return len(c.UserServices) > 0
}

// HasLLMKeys returns true if LLM health check is configured.
func (c Config) HasLLMKeys() bool {
	return len(c.GeminiAPIKeys) > 0 || len(c.LLMCheckModels) > 0
}

// IsLocal returns true if the host is a local machine.
func (c Config) IsLocal() bool {
	h := strings.ToLower(c.Host)
	return h == "local" || h == "localhost" || h == "127.0.0.1"
}

// Init loads config from environment variables.
func Init() Config {
	c := Config{
		Host:                  env("DOZOR_HOST", "local"),
		SSHPort:               envInt("DOZOR_SSH_PORT", defaultSSHPort),
		ComposePath:           env("DOZOR_COMPOSE_PATH", ""),
		Services:              envList("DOZOR_SERVICES", ""),
		Timeout:               envDuration("DOZOR_TIMEOUT", defaultTimeoutSec*time.Second),
		MCPPort:               env("DOZOR_MCP_PORT", "8765"),
		WebhookURL:            env("DOZOR_WEBHOOK_URL", ""),
		WatchInterval:         envDurationStr("DOZOR_WATCH_INTERVAL", defaultWatchIntervalHours*time.Hour),
		CPUThreshold:          envFloat("DOZOR_CPU_THRESHOLD", defaultCPUThreshold),
		MemoryThreshold:       envFloat("DOZOR_MEMORY_THRESHOLD", defaultMemoryThreshold),
		ErrorThreshold:        envInt("DOZOR_ERROR_THRESHOLD", defaultErrorThreshold),
		RestartThreshold:      envInt("DOZOR_RESTART_THRESHOLD", defaultRestartThreshold),
		LogLines:              envInt("DOZOR_LOG_LINES", 100),
		RemoteHost:            env("DOZOR_REMOTE_HOST", ""),
		RemoteURL:             env("DOZOR_REMOTE_URL", ""),
		RemoteServices:        envList("DOZOR_REMOTE_SERVICES", ""),
		RemoteSSHPort:         envInt("DOZOR_REMOTE_SSH_PORT", defaultSSHPort),
		RemoteCheckInterval:   envDurationStr("DOZOR_REMOTE_CHECK_INTERVAL", defaultRemoteCheckIntervalMin*time.Minute),
		SystemdServices:       envList("DOZOR_SYSTEMD_SERVICES", ""),
		RequiredAuthVars:      envList("DOZOR_REQUIRED_AUTH_VARS", ""),
		DiskThreshold:         envFloat("DOZOR_DISK_THRESHOLD", defaultDiskThreshold),
		DiskCritical:          envFloat("DOZOR_DISK_CRITICAL", defaultDiskCritical),
		UserServices:          parseUserServices(env("DOZOR_USER_SERVICES", "")),
		UserServicesUser:      env("DOZOR_USER_SERVICES_USER", ""),
		TrackedBinaries:       parseTrackedBinaries(env("DOZOR_TRACKED_BINARIES", "")),
		GitHubToken:           env("DOZOR_GITHUB_TOKEN", ""),
		BraveAPIKey:           env("DOZOR_BRAVE_API_KEY", ""),
		BraveMaxResults:       envInt("DOZOR_BRAVE_MAX_RESULTS", defaultBraveMaxResults),
		BraveEnabled:          envBool("DOZOR_BRAVE_ENABLED", false),
		DuckDuckGoMaxResults:  envInt("DOZOR_DDG_MAX_RESULTS", defaultBraveMaxResults),
		DuckDuckGoEnabled:     envBool("DOZOR_DDG_ENABLED", true),
		PerplexityAPIKey:      env("DOZOR_PERPLEXITY_API_KEY", ""),
		PerplexityMaxResults:  envInt("DOZOR_PERPLEXITY_MAX_RESULTS", defaultBraveMaxResults),
		PerplexityEnabled:     envBool("DOZOR_PERPLEXITY_ENABLED", false),
		MCPServers:            parseMCPServers(env("DOZOR_MCP_SERVERS", "")),
		KBServer:              env("DOZOR_KB_SERVER", "memdb"),
		KBUser:                env("DOZOR_KB_USER", env("DOZOR_MEMDB_USER", "default")),
		KBCube:                env("DOZOR_KB_CUBE", env("DOZOR_MEMDB_CUBE", "default")),
		KBSearchTool:          env("DOZOR_KB_SEARCH_TOOL", "search_memories"),
		KBSaveTool:            env("DOZOR_KB_SAVE_TOOL", "add_memory"),
		AlertConfirmCount:     envInt("DOZOR_ALERT_CONFIRM_COUNT", 2),
		FlapWindow:            envInt("DOZOR_FLAP_WINDOW", 10),
		FlapHigh:              envFloat("DOZOR_FLAP_HIGH", defaultFlapHigh),
		FlapLow:               envFloat("DOZOR_FLAP_LOW", defaultFlapLow),
		CBKBThreshold:         envInt("DOZOR_CB_KB_THRESHOLD", defaultCBKBThreshold),
		CBKBReset:             envDurationStr("DOZOR_CB_KB_RESET", defaultCBKBResetMin*time.Minute),
		CBLLMThreshold:        envInt("DOZOR_CB_LLM_THRESHOLD", defaultErrorThreshold),
		CBLLMReset:            envDurationStr("DOZOR_CB_LLM_RESET", defaultCBLLMResetMin*time.Minute),
		LLMConfigPath:         env("DOZOR_LLM_CONFIG_PATH", ""),
		LLMCheckURL:           env("DOZOR_LLM_URL", ""),
		LLMCheckAPIKey:        env("DOZOR_LLM_API_KEY", ""),
		LLMCheckModels:        envList("DOZOR_LLM_CHECK_MODELS", ""),
		SuppressWarnings:      parseSuppressWarnings(env("DOZOR_SUPPRESS_WARNINGS", "")),
		InternalPorts:         parseInternalPorts(env("DOZOR_INTERNAL_PORTS", "")),
		RootAllowedContainers: parseRootAllowed(env("DOZOR_ROOT_ALLOWED", "")),
		DangerousHostMounts:   parseDangerousMounts(env("DOZOR_DANGEROUS_MOUNTS", "")),
	}

	// Parse CLIProxyAPI config — fills GeminiAPIKeys and overrides LLMCheckAPIKey if available.
	if c.LLMConfigPath != "" {
		parsed := parseLLMConfig(c.LLMConfigPath)
		c.GeminiAPIKeys = parsed.GeminiAPIKeys
		if parsed.ProxyAPIKey != "" && c.LLMCheckAPIKey == "" {
			c.LLMCheckAPIKey = parsed.ProxyAPIKey
		}
	}

	return c
}
