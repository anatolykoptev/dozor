package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool-call timeout tiers. Assign a tier to Config.ToolTimeout or to entries in
// Config.ToolTimeouts, or select the global default tier via Config.ToolTimeoutMode.
const (
	ToolTimeoutShort   = 60 * time.Second  // fast tools (single query / cache read)
	ToolTimeoutDefault = 90 * time.Second  // default tier
	ToolTimeoutLong    = 180 * time.Second // scrape + LLM-extraction tools
)

// ToolTimeoutMode selects a named default tool-call timeout tier. It is applied
// only when Config.ToolTimeout is 0 (unset). An explicit Config.ToolTimeout, or
// a per-tool Config.ToolTimeouts entry, always wins — that is the "custom" tier.
type ToolTimeoutMode string

const (
	ToolTimeoutModeShort   ToolTimeoutMode = "short"   // ToolTimeoutShort (60s)
	ToolTimeoutModeDefault ToolTimeoutMode = "default" // ToolTimeoutDefault (90s); also the zero-value behaviour
	ToolTimeoutModeLong    ToolTimeoutMode = "long"    // ToolTimeoutLong (180s)
	ToolTimeoutModeCustom  ToolTimeoutMode = "custom"  // use Config.ToolTimeout exactly as the app set it
)

const (
	defaultPort               = "8080"
	defaultReadTimeout        = 30 * time.Second
	defaultWriteTimeout       = 0               // disabled for SSE — tools manage own timeout via context
	defaultIdleTimeout        = 5 * time.Minute // generous for pauses between tool calls
	defaultShutdownTimeout    = 10 * time.Second
	defaultToolTimeout        = ToolTimeoutDefault
	defaultMaxConcurrentTools = 100
	portEnvVar                = "MCP_PORT"
)

// defaultMaxRequestBodyBytes is go-mcpserver's own cap for POST /mcp bodies
// when Config.MaxRequestBodyBytes is 0: 16 MiB, so base64 payloads that worked
// on go-sdk v1.6.1 (no limit) keep working. The go-sdk v1.8.0 default is 4 MiB.
const defaultMaxRequestBodyBytes int64 = 16 << 20

// Config controls how the MCP server runs.
type Config struct {
	Name    string // service name for /health + logs (required)
	Version string // version for /health + logs (required)
	Host    string // bind host; empty → "0.0.0.0" (all interfaces)
	Port    string // HTTP port; empty → MCP_PORT env → "8080"

	WriteTimeout    time.Duration // default 0 (disabled for SSE compat; tools manage own timeout)
	IdleTimeout     time.Duration // default 5m; Go net/http uses ReadTimeout as keep-alive idle if IdleTimeout==0
	ReadTimeout     time.Duration // default 30s
	ShutdownTimeout time.Duration // default 10s

	Metrics func() string        // if set, registers GET /metrics
	Routes  func(*http.ServeMux) // extra routes after /mcp, /health, /metrics

	Middleware       []Middleware // custom middleware, applied after built-ins
	CORSOrigins      []string     // nil = no CORS; ["*"] = allow all
	CORSMaxAge       int          // preflight Max-Age in seconds; 0 = omit header
	CORSAllowHeaders []string     // nil = default (Content-Type, Authorization, X-Request-ID)
	ReadinessCheck   func() error // nil = /health/ready always returns 200

	DisableRecovery   bool  // default false (recovery ON)
	DisableHealth     bool  // set true to register custom /health in Routes
	DisableRequestLog bool  // default false (request logging ON)
	DisableMCP        bool  // skip /mcp route registration; server param may be nil
	Stateless         *bool // nil = default true; *false = stateful (session) mode

	// LogSkipPaths lists URL paths that RequestLog should NOT emit Info logs
	// for. Matches by exact path. nil → defaults to /health, /health/live,
	// /health/ready, /metrics. Set LogSkipDefaults=true to disable all default
	// skipping. Skipped paths are still served normally; only the access-log
	// Info entry is suppressed (a Debug-level entry is still emitted).
	LogSkipPaths    []string
	LogSkipDefaults bool // true → never apply default skip list, only LogSkipPaths is used

	MCPReceivingMiddleware []mcp.Middleware // applied to incoming JSON-RPC (client→server)
	MCPSendingMiddleware   []mcp.Middleware // applied to outgoing JSON-RPC (server→client)

	ToolTimeout        time.Duration            // default tool execution timeout; 0 = resolve from ToolTimeoutMode (default 90s); tools can override via ToolTimeouts
	ToolTimeouts       map[string]time.Duration // per-tool timeout overrides; key = tool name
	MaxToolTimeout     time.Duration            // upper bound for timeout_secs arg override; 0 = ToolTimeout * 2
	MaxConcurrentTools int                      // max concurrent tool execution goroutines; 0 = 100

	// ToolTimeoutMode selects the default tool-call timeout tier (short 60s /
	// default 90s / long 180s / custom) used when ToolTimeout == 0. An explicit
	// ToolTimeout or a per-tool ToolTimeouts entry always wins. Empty = default.
	ToolTimeoutMode ToolTimeoutMode

	// ToolKeepaliveInterval, when > 0, makes the server emit a periodic MCP
	// progress notification while a tool call is running, so long tools keep the
	// response stream warm and clients/proxies don't abandon a call still in
	// progress. 0 = disabled (default).
	//
	// Requires SSE mode (JSONResponse == false): the notification routes to the
	// tool call's event stream. In application/json mode there is no per-request
	// stream and the heartbeat is a harmless no-op. Notifications are used (not
	// server->client ping requests) because requests are rejected in stateless mode.
	ToolKeepaliveInterval time.Duration

	SessionTimeout    time.Duration  // idle session timeout; 0 = never (passed to StreamableHTTPOptions)
	EventStore        mcp.EventStore // stream resumption; nil = MemoryEventStore (auto-enabled unless DisableEventStore)
	DisableEventStore bool           // true = do not auto-enable MemoryEventStore when EventStore is nil
	JSONResponse      bool           // true = application/json instead of text/event-stream
	MCPLogger         *slog.Logger   // separate logger for StreamableHTTP handler; nil = none

	// DisableLocalhostProtection disables the SDK's automatic DNS rebinding
	// protection. By default, requests from 127.0.0.1/[::1] with a non-localhost
	// Host header are rejected with 403. Set true ONLY if behind a trusted
	// reverse proxy on localhost that you control.
	DisableLocalhostProtection bool

	// CancelOnClientDisconnect makes the tool handler's context cancel when the
	// client aborts the POST (disconnect or cancel), so a tool whose caller is
	// gone stops instead of running to ToolTimeout. nil = true; set to a pointer
	// to false for the old behaviour. Applies to stateless handlers only (the
	// default): go-sdk's StreamableHTTPOptions.PropagateRequestCancellation
	// covers 2026-07-28 requests, and a go-mcpserver wrapper covers
	// 2025-11-25 and older ones. Stateful mode is unchanged - a session may
	// resume a dropped POST stream.
	//
	// Trade-off: a tool cancelled midway must already be idempotent or
	// transactional - ToolTimeout can cut it at any point in the same way.
	CancelOnClientDisconnect *bool

	// MaxRequestBodyBytes caps the size of a single POST /mcp request body;
	// larger requests are rejected with 413 Request Entity Too Large. 0 = 16 MiB
	// (go-mcpserver default; go-sdk v1.8.0 itself defaults to 4 MiB and v1.6.1
	// had no limit). A negative value disables the limit entirely (go-sdk
	// semantics) - do not use that on servers exposed to untrusted clients.
	// Honoured by Run and Build as well as Serve.
	MaxRequestBodyBytes int64

	// SupportedProtocolVersions restricts the MCP protocol versions the server
	// advertises and negotiates (e.g. {"2025-11-25"} to opt out of 2026-07-28
	// without a code revert). nil/empty = every version the SDK supports. The
	// list can only narrow, never widen; a version the SDK does not implement
	// makes NewServer panic at startup (go-sdk behaviour). Applied only at
	// server creation by NewServer/Serve; a value set on a Config passed to
	// Run/Build is ignored (see the startup warning).
	SupportedProtocolVersions []string

	// KeepAlive sets the interval for periodic ping requests. If the peer
	// fails to respond, the session is automatically closed. 0 = disabled.
	// Recommended for stateful mode: 30s. Applied only at server creation by
	// NewServer/Serve; a value set on a Config passed to Run/Build is ignored
	// (see the startup warning).
	KeepAlive time.Duration

	// SchemaCache caches JSON schemas to avoid repeated reflection. Useful
	// for stateless deployments where a new Server is created per request.
	// Create once with mcp.NewSchemaCache() and share across servers. Applied
	// only at server creation by NewServer/Serve; a value set on a Config
	// passed to Run/Build is ignored (see the startup warning).
	SchemaCache *mcp.SchemaCache

	BearerAuth *BearerAuth // nil = no auth; wraps /mcp only (see auth.go)
	RESTBridge bool        // enable /api/tools/* REST endpoints (auto-generated from MCP tools)
	RESTPrefix string      // URL prefix for REST endpoints; default "/api"

	Context    context.Context // nil → internal signal.NotifyContext(SIGINT, SIGTERM)
	Logger     *slog.Logger    // nil → auto (stdout HTTP / stderr stdio, LevelInfo)
	OnShutdown func()          // called before HTTP shutdown

	// Transport selects the MCP transport. TransportStdio ("stdio") runs over
	// stdin/stdout; "" (empty) or TransportHTTP ("http") = HTTP. Prefer
	// passing a parsed flag value over relying on the legacy --stdio os.Args
	// scan — a cobra/pflag layer would reject the flag before Run ever sees
	// it (vaelor #857).
	Transport string

	// onRESTBridgeCleanup is set internally by Run() to receive the REST bridge
	// cleanup function from buildHandler, so it can be called AFTER srv.Shutdown()
	// completes (not on signal receipt, which would close sessions mid-request).
	onRESTBridgeCleanup *func()
}

func validate(cfg Config) error {
	if cfg.Name == "" {
		return errors.New("mcpserver: Config.Name is required")
	}
	if cfg.Version == "" {
		return errors.New("mcpserver: Config.Version is required")
	}
	if cfg.Transport != "" && cfg.Transport != TransportStdio && cfg.Transport != TransportHTTP {
		return fmt.Errorf("mcpserver: unknown transport %q", cfg.Transport)
	}
	return nil
}

func withDefaults(cfg Config) Config {
	if cfg.Port == "" {
		if p := os.Getenv(portEnvVar); p != "" {
			cfg.Port = p
		} else {
			cfg.Port = defaultPort
		}
	}
	applyTimeoutDefaults(&cfg)
	if cfg.MaxRequestBodyBytes == 0 {
		cfg.MaxRequestBodyBytes = defaultMaxRequestBodyBytes
	}
	if !cfg.LogSkipDefaults && cfg.LogSkipPaths == nil {
		cfg.LogSkipPaths = defaultLogSkipPaths()
	}
	// Enable stream resumption by default — prevents lost events after reconnect.
	// DisableEventStore allows consumers to opt out (e.g. for stateless deployments
	// where the 10 MiB MemoryEventStore limit causes 500s under high load).
	if cfg.EventStore == nil && !cfg.DisableEventStore {
		cfg.EventStore = mcp.NewMemoryEventStore(nil)
	}
	// Warn about unbounded session memory growth in stateful mode.
	stateless := cfg.Stateless == nil || *cfg.Stateless
	if !stateless && cfg.SessionTimeout == 0 {
		slog.Warn("Config.Stateless=false with SessionTimeout=0 — sessions never expire and memory grows unbounded. Set SessionTimeout (e.g. 30m) for stateful mode.")
	}
	return cfg
}

// withoutServerOpts returns a copy with the server-creation-only options
// cleared, so Run/Build (which ignore them) don't warn about them.
func (c Config) withoutServerOpts() Config {
	c.KeepAlive = 0
	c.SchemaCache = nil
	c.SupportedProtocolVersions = nil
	return c
}

// warnIgnoredServerOpts warns when server-creation-only options are set on a
// Config passed to Run/Build, since those options are only honoured by
// NewServer/Serve.
func warnIgnoredServerOpts(cfg Config, logger *slog.Logger) {
	if cfg.KeepAlive != 0 || cfg.SchemaCache != nil || len(cfg.SupportedProtocolVersions) > 0 {
		logger.Warn("mcpserver: Config.KeepAlive/SchemaCache/SupportedProtocolVersions set on Run/Build are ignored — server options apply only via NewServer/Serve; set them there or remove them from the Run/Build Config")
	}
}

// applyTimeoutDefaults fills in zero-valued timeout/concurrency fields with
// their defaults. Extracted from withDefaults to keep cyclomatic complexity
// under the cyclop limit.
func applyTimeoutDefaults(cfg *Config) {
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = resolveToolTimeoutMode(cfg.ToolTimeoutMode)
	}
	if cfg.MaxToolTimeout == 0 {
		cfg.MaxToolTimeout = cfg.ToolTimeout * 2
	}
	if cfg.MaxConcurrentTools == 0 {
		cfg.MaxConcurrentTools = defaultMaxConcurrentTools
	}
}

// resolveToolTimeoutMode maps a ToolTimeoutMode to its tier duration. It is
// consulted only when Config.ToolTimeout is unset. An unknown or empty mode —
// and "custom" without an explicit ToolTimeout — falls back to the default tier.
func resolveToolTimeoutMode(mode ToolTimeoutMode) time.Duration {
	switch mode {
	case ToolTimeoutModeShort:
		return ToolTimeoutShort
	case ToolTimeoutModeLong:
		return ToolTimeoutLong
	default:
		return ToolTimeoutDefault
	}
}

func applyMCPMiddleware(server *mcp.Server, cfg Config) {
	if server == nil {
		return
	}
	// Tool timeout middleware — always first so it wraps everything.
	server.AddReceivingMiddleware(ToolTimeoutMiddleware(cfg))

	if cfg.BearerAuth != nil && cfg.BearerAuth.ToolFilter != nil {
		server.AddReceivingMiddleware(toolFilterMiddleware(cfg.BearerAuth.ToolFilter))
	}
	if len(cfg.MCPReceivingMiddleware) > 0 {
		server.AddReceivingMiddleware(cfg.MCPReceivingMiddleware...)
	}
	if len(cfg.MCPSendingMiddleware) > 0 {
		server.AddSendingMiddleware(cfg.MCPSendingMiddleware...)
	}
	// Registered last = outermost: sees the tools/list result after every
	// other middleware (e.g. ToolFilter) has shaped it.
	server.AddReceivingMiddleware(portableToolsListMiddleware())
}

func defaultLogSkipPaths() []string {
	return []string{"/health", "/health/live", "/health/ready", "/metrics"}
}

func buildMiddleware(cfg Config, logger *slog.Logger) []Middleware {
	var mws []Middleware
	if !cfg.DisableRecovery {
		mws = append(mws, Recovery(logger))
	}
	mws = append(mws, RequestID())
	if !cfg.DisableRequestLog {
		mws = append(mws, RequestLogWithSkip(logger, cfg.LogSkipPaths))
	}
	if len(cfg.CORSOrigins) > 0 {
		mws = append(mws, CORS(CORSConfig{
			Origins:      cfg.CORSOrigins,
			MaxAge:       cfg.CORSMaxAge,
			AllowHeaders: cfg.CORSAllowHeaders,
		}))
	}
	mws = append(mws, cfg.Middleware...)
	return mws
}
