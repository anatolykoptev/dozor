package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultPort               = "8080"
	defaultReadTimeout        = 30 * time.Second
	defaultWriteTimeout       = 0               // disabled for SSE — tools manage own timeout via context
	defaultIdleTimeout        = 5 * time.Minute // generous for pauses between tool calls
	defaultShutdownTimeout    = 10 * time.Second
	defaultToolTimeout        = 90 * time.Second
	defaultMaxConcurrentTools = 100
	portEnvVar                = "MCP_PORT"
)

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

	ToolTimeout        time.Duration            // default tool execution timeout; 0 = 90s; tools can override via ToolTimeouts
	ToolTimeouts       map[string]time.Duration // per-tool timeout overrides; key = tool name
	MaxToolTimeout     time.Duration            // upper bound for timeout_secs arg override; 0 = ToolTimeout * 2
	MaxConcurrentTools int                      // max concurrent tool execution goroutines; 0 = 100

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

	// KeepAlive sets the interval for periodic ping requests. If the peer
	// fails to respond, the session is automatically closed. 0 = disabled.
	// Recommended for stateful mode: 30s. Applied via ConfigureServer.
	KeepAlive time.Duration

	// SchemaCache caches JSON schemas to avoid repeated reflection. Useful
	// for stateless deployments where a new Server is created per request.
	// Create once with mcp.NewSchemaCache() and share across servers.
	// Applied via ConfigureServer.
	SchemaCache *mcp.SchemaCache

	BearerAuth *BearerAuth // nil = no auth; wraps /mcp only (see auth.go)
	RESTBridge bool        // enable /api/tools/* REST endpoints (auto-generated from MCP tools)
	RESTPrefix string      // URL prefix for REST endpoints; default "/api"

	Context    context.Context // nil → internal signal.NotifyContext(SIGINT, SIGTERM)
	Logger     *slog.Logger    // nil → auto (stdout HTTP / stderr stdio, LevelInfo)
	OnShutdown func()          // called before HTTP shutdown

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
		cfg.ToolTimeout = defaultToolTimeout
	}
	if cfg.MaxToolTimeout == 0 {
		cfg.MaxToolTimeout = cfg.ToolTimeout * 2
	}
	if cfg.MaxConcurrentTools == 0 {
		cfg.MaxConcurrentTools = defaultMaxConcurrentTools
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
