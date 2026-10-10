# go-mcpserver

Bootstrap library for Go MCP servers. One `Run()` call instead of ~80 lines of boilerplate.

Built on top of [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk).

## Install

```bash
go get github.com/anatolykoptev/go-mcpserver@latest
```

## Usage

```go
package main

import (
	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	// Use mcpserver.NewServer to get KeepAlive + SchemaCache support.
	// If you don't need those, mcp.NewServer(impl, nil) + mcpserver.Run also works.
	server := mcpserver.NewServer(&mcp.Implementation{
		Name:    "my-service",
		Version: version,
	}, mcpserver.Config{
		Name:    "my-service",
		Version: version,
	})

	// register tools...

	if err := mcpserver.Run(server, mcpserver.Config{
		Name:    "my-service",
		Version: version,
	}); err != nil {
		panic(err)
	}
}
```

This gives you:

- **Stdio detection** — pass `--stdio` flag to run via stdin/stdout
- **Structured logging** — slog to stdout (HTTP) or stderr (stdio)
- **Signal handling** — SIGINT/SIGTERM with graceful shutdown
- **MCP routes** — `/mcp` and `/mcp/` with stateless StreamableHTTP
- **Health endpoints** — `/health`, `/health/live`, `/health/ready`
- **Middleware chain** — Recovery, Request ID, Request logging, CORS
- **Config validation** — `Name` and `Version` are required
- **Configurable timeouts** — read (30s), write (120s), shutdown (10s)

## Config

```go
type Config struct {
	Name    string // service name (required)
	Version string // version string (required)
	Port    string // HTTP port; empty → MCP_PORT env → "8080"

	WriteTimeout    time.Duration // default 0 (disabled for SSE compat; tools manage own timeout)
	ReadTimeout     time.Duration // default 30s
	ShutdownTimeout time.Duration // default 10s

	Metrics func() string        // if set, registers GET /metrics
	Routes  func(*http.ServeMux) // extra routes after /mcp, /health, /metrics

	Middleware        []Middleware  // custom middleware, applied after built-ins
	CORSOrigins       []string     // nil = no CORS; ["*"] = allow all
	CORSMaxAge        int          // preflight Max-Age in seconds; 0 = omit
	CORSAllowHeaders  []string     // nil = default (Content-Type, Authorization, X-Request-ID, Mcp-Protocol-Version, Mcp-Session-Id, Last-Event-ID, Mcp-Method, Mcp-Name); Mcp-Session-Id is also exposed
	ReadinessCheck    func() error // nil = /health/ready always returns 200

	DisableRecovery   bool            // default false (recovery ON)
	DisableHealth     bool            // set true to register custom /health in Routes
	DisableRequestLog bool            // default false (request logging ON)

	// MCP session options
	KeepAlive                time.Duration     // ping interval; 0 = disabled; use NewServer to apply
	SchemaCache              *mcp.SchemaCache  // JSON schema cache for stateless mode; use NewServer to apply
	SupportedProtocolVersions []string         // nil = all SDK versions; {"2025-11-25"} opts out of 2026-07-28; use NewServer to apply
	DisableLocalhostProtection bool            // DNS rebinding protection; set true ONLY behind trusted reverse proxy
	MaxRequestBodyBytes       int64            // POST /mcp body cap; 0 = 16 MiB, <0 = unlimited (applies in Run/Build/Serve)
	CancelOnClientDisconnect  *bool            // nil = true: cancel the tool ctx when the client aborts the POST (stateless only); *false = old behaviour

	Context    context.Context // nil → internal signal.NotifyContext(SIGINT, SIGTERM)
	Logger     *slog.Logger    // nil → auto
	OnShutdown func()          // called before HTTP shutdown
}
```

## NewServer vs mcp.NewServer

`mcpserver.NewServer(impl, cfg)` creates an `*mcp.Server` with `ServerOptions`
derived from `Config`:

| Config field | ServerOptions field | Purpose |
|---|---|---|
| `KeepAlive` | `KeepAlive` | Periodic ping; auto-closes session if peer doesn't respond |
| `SchemaCache` | `SchemaCache` | Caches JSON schemas; avoids repeated reflection in stateless mode |
| `SupportedProtocolVersions` | `SupportedProtocolVersions` | Narrows the MCP protocol versions the server negotiates (rollback lever); an unknown version panics at startup naming the valid set |

If you use `mcp.NewServer(impl, nil)` directly, these fields are ignored — `Run`/`Build`
apply middleware (ToolTimeout, ToolFilter, custom) regardless, but `KeepAlive`,
`SchemaCache` and `SupportedProtocolVersions` can only be set at server creation
time. `Run`/`Build` log a warning when any of them is set on their Config.

`MaxRequestBodyBytes` is **not** NewServer-only: it configures the HTTP handler, so
`Run`, `Build` and `Serve` all honour it.

## Upgrading to v0.20 / go-sdk v1.8

v0.20 moves from go-sdk v1.6.1 to v1.8.0 (MCP protocol 2026-07-28). No Go API
changed, but consumers can notice:

- **2026-07-28 is served by default.** Stateless servers (the default) answer
  `server/discover` and per-request `_meta` requests; 2025-11-25 and older clients
  keep working. Python MCP SDK 2.0 clients now connect.
- **Stateless servers issue no session IDs to ANY client**, including 2025
  clients: `req.Session.ID()` is `""` and `DELETE /mcp` returns `405`. Temporary
  SDK opt-out (removed in go-sdk v1.9.0): `MCPGODEBUG=allowsessionsinstateless=1`.
  Code that keys state on the session ID must stop doing so or run stateful.
- **Request body cap.** go-sdk v1.8.0 caps POST bodies at 4 MiB (v1.6.1 had no
  limit). go-mcpserver sets its own default of **16 MiB** via
  `Config.MaxRequestBodyBytes`; larger requests get `413`. `0` = 16 MiB, a
  negative value disables the cap (untrusted-client servers should not). The
  `mcpclient` package surfaces a 413 or any JSON-RPC error as `ErrRejected`, never
  as `ErrUnreachable`, so `WithUnreachableTolerant(true)` no longer hides it.
- **Stateful servers (`Stateless=false`)** answer a 2026-07-28 request with a
  JSON-RPC `-32022` (UnsupportedProtocolVersion) error listing the legacy versions,
  instead of a plain-text 400; SDK clients renegotiate down on their own.
  (`MCPGODEBUG=plaintextstatefulrejection=1` restores the old body, until v1.9.0.)
- `ToolAnnotations.ReadOnlyHint` / `IdempotentHint` are always serialized, even
  when `false` (`MCPGODEBUG=hintomitempty=1` restores omitempty, until v1.9.0).
- Invalid tool/method params return JSON-RPC `-32602` (wrapped) instead of a raw
  error.
- List results (`tools/list`, `prompts/list`, `resources/list`, `server/discover`)
  carry `ttlMs` and `cacheScope`.
- On 2026-07-28 sessions `ping`, `logging/setLevel` and `resources/subscribe` /
  `unsubscribe` return `MethodNotFound`, and server-to-client requests
  (sampling, elicitation, roots) go through multi-round-trip requests (MRTR)
  instead of fresh JSON-RPC requests. `ToolKeepaliveInterval` progress
  notifications are unaffected.
- Default CORS allow-headers now include `Mcp-Protocol-Version`, `Mcp-Session-Id`,
  `Last-Event-ID`, `Mcp-Method`, `Mcp-Name` and `Mcp-Session-Id` is exposed.
  `Mcp-Param-*` (tools using `x-mcp-header`) is a dynamic prefix: list those
  names in `CORSAllowHeaders`.
- **Rollback lever:** `Config.SupportedProtocolVersions: []string{"2025-11-25"}`
  (via `NewServer`/`Serve`) pins a service to the old protocol without a code
  revert. There is deliberately no env var.
- `Config.EventStore` / `DisableEventStore` still compile and apply to 2025-11-25
  traffic; resumability does not exist on 2026-07-28.

**Stateless mode + SchemaCache:**

```go
cache := mcp.NewSchemaCache() // create once, share across requests

func handler(w http.ResponseWriter, r *http.Request) {
    server := mcpserver.NewServer(impl, mcpserver.Config{
        SchemaCache: cache,
        Stateless:   &[]bool{true}[0],
    })
    // ... register tools, handle request ...
}
```

**Stateful mode + KeepAlive:**

```go
server := mcpserver.NewServer(impl, mcpserver.Config{
    KeepAlive: 30 * time.Second,
})
mcpserver.Run(server, mcpserver.Config{
    Name:    "my-service",
    Version: "1.0.0",
})
```

## Cancelling tools when the client disconnects

By default (stateless mode) a tool handler's `ctx` is cancelled as soon as the
client aborts its POST (disconnect or cancel), instead of running until
`ToolTimeout`. A caller that is gone cannot receive the result, so long tools
(browser automation, research, LLM calls) stop burning CPU and money. It works
for 2026-07-28 requests via go-sdk's `PropagateRequestCancellation` and for
2025-11-25 and older requests via a go-mcpserver wrapper. The tool-timeout
middleware and the keepalive goroutine exit with the context.

Set `CancelOnClientDisconnect: new(bool)` to keep the old behaviour. **Trade-off:**
a tool with side effects that is cancelled midway must already be idempotent or
transactional - `ToolTimeout` can cut it at any point in exactly the same way.
Stateful mode (`Stateless=false`) is unchanged: a session may resume a dropped
stream.

## Reverse proxy on localhost

By default, the SDK rejects requests from `127.0.0.1`/`[::1]` with a non-localhost
`Host` header (DNS rebinding protection). If you run behind a trusted reverse proxy
on the same host, set `DisableLocalhostProtection: true`:

```go
mcpserver.Run(server, mcpserver.Config{
    Name:                     "my-service",
    Version:                  "1.0.0",
    DisableLocalhostProtection: true, // behind trusted nginx/Caddy on localhost
})
```

## Health Endpoints

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Basic health: `{"status":"ok","service":"...","version":"..."}` |
| `GET /health/live` | Liveness probe: always returns 200 |
| `GET /health/ready` | Readiness probe: calls `ReadinessCheck`, returns 200 or 503 |

## Middleware

Built-in middleware (applied in order):

1. **Recovery** — catches panics, returns 500 (disable: `DisableRecovery`)
2. **RequestID** — generates/propagates `X-Request-ID` header
3. **RequestLog** — logs method, path, status, duration (disable: `DisableRequestLog`)
4. **CORS** — Cross-Origin Resource Sharing (enable: set `CORSOrigins`)

Custom middleware is appended after built-ins via `Config.Middleware`.

### Exported middleware

```go
mcpserver.Recovery(logger)        // panic recovery
mcpserver.RequestID()             // X-Request-ID generation
mcpserver.RequestLog(logger)      // request logging
mcpserver.CORS(mcpserver.CORSConfig{...}) // CORS
mcpserver.Chain(handler, mw...)   // apply middleware chain
mcpserver.RequestIDFromContext(ctx) // retrieve request ID
```

## Examples

### External context (no double signal handler)

```go
ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer cancel()

// ... init DB, caches using ctx ...

mcpserver.Run(server, mcpserver.Config{
	Name:    "my-service",
	Version: version,
	Context: ctx, // reuse parent context
})
```

### CORS with Max-Age

```go
mcpserver.Run(server, mcpserver.Config{
	Name:        "my-api",
	Version:     version,
	CORSOrigins: []string{"https://app.example.com"},
	CORSMaxAge:  3600, // cache preflight for 1 hour
})
```

### Readiness check with DB ping

```go
mcpserver.Run(server, mcpserver.Config{
	Name:    "my-service",
	Version: version,
	ReadinessCheck: func() error {
		return db.Ping(context.Background())
	},
})
```

### Custom metrics and extra routes

```go
mcpserver.Run(server, mcpserver.Config{
	Name:         "go-wp",
	Version:      version,
	Port:         "8894",
	WriteTimeout: 600 * time.Second,
	Metrics:      engine.FormatMetrics,
	Routes: func(mux *http.ServeMux) {
		mux.HandleFunc("POST /cache/clear", handleCacheClear)
	},
	OnShutdown: func() {
		wpserver.Shutdown()
	},
})
```

### Pre-Run stdio check

```go
if mcpserver.IsStdio() {
	// skip heavy init (DB pools, caches) in stdio mode
}
```

## License

Apache 2.0
