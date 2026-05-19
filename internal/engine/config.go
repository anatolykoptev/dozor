package engine

import (
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/llmcfg"
)

const (
	defaultSSHPort    = 1987
	sshStandardPort   = 22
	defaultTimeoutSec = 30
	// defaultErrorThreshold is the minimum number of errors in the 5-minute
	// staleness window before an error-count alert fires. Bumped from 5 → 10
	// (fix/alert-pattern-tuning) because Chrome-wrapper services (cloakbrowser)
	// and retry-loop services produce bursts of 5 within a single transient
	// event, creating false-positive alerts before the noise rules can filter.
	// Real incidents (container exit, disk full) are caught by state-level
	// checks independent of this threshold.
	defaultErrorThreshold         = 10
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
	defaultRestartThreshold       = 5
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
	KBPersonID   string // Phase 2 split: person identity (defaults to KBUser if unset)
	KBCube       string
	KBSearchTool string
	KBSaveTool   string

	AlertConfirmCount int
	// RemoteAlertConfirmCount overrides AlertConfirmCount specifically for the
	// remote watch (SSH-based health probes against external servers). Higher
	// values absorb transient SSH timeouts. Falls back to AlertConfirmCount
	// when unset (0).
	RemoteAlertConfirmCount int
	FlapWindow              int
	FlapHigh                float64
	FlapLow                 float64

	CBKBThreshold  int
	CBKBReset      time.Duration
	CBLLMThreshold int
	CBLLMReset     time.Duration

	LLMConfigPath  string
	GeminiAPIKeys  []string
	LLMCheckURL    string
	LLMCheckAPIKey string
	LLMCheckModels []string

	// LLMCfg is the canonical resolved LLM configuration (llmcfg.Resolve output).
	// Populated by Init() via llmcfg.Resolve; subsystems should prefer this over
	// reading individual env vars directly.
	LLMCfg llmcfg.Config

	SuppressWarnings      map[string]string
	InternalPorts         map[string]string
	RootAllowedContainers map[string]bool
	DangerousHostMounts   []string

	// Nuclei vulnerability scanning
	NucleiSeverities string // comma-separated list: critical,high,medium,low,info
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

// EffectiveRemoteConfirmCount returns the threshold for remote watch failure
// confirmation, falling back to AlertConfirmCount when the remote-specific
// override is unset (0) or negative.
func (c Config) EffectiveRemoteConfirmCount() int {
	if c.RemoteAlertConfirmCount > 0 {
		return c.RemoteAlertConfirmCount
	}
	return c.AlertConfirmCount
}

// IsLocal returns true if the host is a local machine.
func (c Config) IsLocal() bool {
	h := strings.ToLower(c.Host)
	return h == "local" || h == "localhost" || h == "127.0.0.1"
}

// Init loads config from environment variables.
func Init() Config {
	c := Config{
		Host:                    env("DOZOR_HOST", "local"),
		SSHPort:                 envInt("DOZOR_SSH_PORT", defaultSSHPort),
		ComposePath:             env("DOZOR_COMPOSE_PATH", ""),
		Services:                envList("DOZOR_SERVICES", ""),
		Timeout:                 envDuration("DOZOR_TIMEOUT", defaultTimeoutSec*time.Second),
		MCPPort:                 env("DOZOR_MCP_PORT", "8765"),
		WebhookURL:              env("DOZOR_WEBHOOK_URL", ""),
		WatchInterval:           envDurationStr("DOZOR_WATCH_INTERVAL", defaultWatchIntervalHours*time.Hour),
		CPUThreshold:            envFloat("DOZOR_CPU_THRESHOLD", defaultCPUThreshold),
		MemoryThreshold:         envFloat("DOZOR_MEMORY_THRESHOLD", defaultMemoryThreshold),
		ErrorThreshold:          envInt("DOZOR_ERROR_THRESHOLD", defaultErrorThreshold),
		RestartThreshold:        envInt("DOZOR_RESTART_THRESHOLD", defaultRestartThreshold),
		LogLines:                envInt("DOZOR_LOG_LINES", 100),
		RemoteHost:              env("DOZOR_REMOTE_HOST", ""),
		RemoteURL:               env("DOZOR_REMOTE_URL", ""),
		RemoteServices:          envList("DOZOR_REMOTE_SERVICES", ""),
		RemoteSSHPort:           envInt("DOZOR_REMOTE_SSH_PORT", defaultSSHPort),
		RemoteCheckInterval:     envDurationStr("DOZOR_REMOTE_CHECK_INTERVAL", defaultRemoteCheckIntervalMin*time.Minute),
		SystemdServices:         envList("DOZOR_SYSTEMD_SERVICES", ""),
		RequiredAuthVars:        envList("DOZOR_REQUIRED_AUTH_VARS", ""),
		DiskThreshold:           envFloat("DOZOR_DISK_THRESHOLD", defaultDiskThreshold),
		DiskCritical:            envFloat("DOZOR_DISK_CRITICAL", defaultDiskCritical),
		UserServices:            parseUserServices(env("DOZOR_USER_SERVICES", "")),
		UserServicesUser:        env("DOZOR_USER_SERVICES_USER", ""),
		TrackedBinaries:         parseTrackedBinaries(env("DOZOR_TRACKED_BINARIES", "")),
		GitHubToken:             env("DOZOR_GITHUB_TOKEN", ""),
		BraveAPIKey:             env("DOZOR_BRAVE_API_KEY", ""),
		BraveMaxResults:         envInt("DOZOR_BRAVE_MAX_RESULTS", defaultBraveMaxResults),
		BraveEnabled:            envBool("DOZOR_BRAVE_ENABLED", false),
		DuckDuckGoMaxResults:    envInt("DOZOR_DDG_MAX_RESULTS", defaultBraveMaxResults),
		DuckDuckGoEnabled:       envBool("DOZOR_DDG_ENABLED", true),
		PerplexityAPIKey:        env("DOZOR_PERPLEXITY_API_KEY", ""),
		PerplexityMaxResults:    envInt("DOZOR_PERPLEXITY_MAX_RESULTS", defaultBraveMaxResults),
		PerplexityEnabled:       envBool("DOZOR_PERPLEXITY_ENABLED", false),
		MCPServers:              parseMCPServers(env("DOZOR_MCP_SERVERS", "")),
		AlertConfirmCount:       envInt("DOZOR_ALERT_CONFIRM_COUNT", 2),
		RemoteAlertConfirmCount: envInt("DOZOR_REMOTE_ALERT_CONFIRM_COUNT", 0),
		FlapWindow:              envInt("DOZOR_FLAP_WINDOW", 10),
		FlapHigh:                envFloat("DOZOR_FLAP_HIGH", defaultFlapHigh),
		FlapLow:                 envFloat("DOZOR_FLAP_LOW", defaultFlapLow),
		LLMConfigPath:           env("DOZOR_LLM_CONFIG_PATH", ""),
		// LLMCheckURL, LLMCheckAPIKey, LLMCheckModels, GeminiAPIKeys are
		// back-filled below from llmcfg.Resolve (env > YAML > defaults).
		SuppressWarnings:      parseSuppressWarnings(env("DOZOR_SUPPRESS_WARNINGS", "")),
		InternalPorts:         parseInternalPorts(env("DOZOR_INTERNAL_PORTS", "")),
		RootAllowedContainers: parseRootAllowed(env("DOZOR_ROOT_ALLOWED", "")),
		DangerousHostMounts:   parseDangerousMounts(env("DOZOR_DANGEROUS_MOUNTS", "")),
		NucleiSeverities:      env("DOZOR_NUCLEI_SEVERITIES", "critical,high,medium"),
	}

	c.KBServer, c.KBUser, c.KBPersonID, c.KBCube, c.KBSearchTool, c.KBSaveTool = initKBConfig()
	c.CBKBThreshold, c.CBLLMThreshold, c.CBKBReset, c.CBLLMReset = initCBConfig()

	// Resolve canonical LLM config via llmcfg (env > YAML > defaults precedence).
	// Replaces the inline parseLLMConfig call that previously read env vars directly.
	llmCfg, err := llmcfg.Resolve(c.LLMConfigPath)
	if err != nil {
		slog.Warn("llmcfg: resolve failed, using partial config", slog.Any("error", err))
	}
	c.LLMCfg = llmCfg

	// Back-fill legacy engine.Config LLM fields from the canonical Config so
	// existing callers (CheckLLMKeys, watch.go) continue to work unchanged.
	c.GeminiAPIKeys = llmCfg.GeminiKeys
	c.LLMCheckURL = llmCfg.CheckURL
	c.LLMCheckAPIKey = llmCfg.CheckKey
	c.LLMCheckModels = llmCfg.CheckModels

	return c
}
