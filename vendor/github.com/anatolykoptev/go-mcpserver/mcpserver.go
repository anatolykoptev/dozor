// Package mcpserver provides a bootstrap library for Go MCP servers.
//
// It handles stdio detection, slog setup, signal handling, StreamableHTTP handler,
// middleware chain, /health endpoints, and graceful shutdown.
package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IsStdio reports whether --stdio was passed on the command line.
func IsStdio() bool {
	return isStdio()
}

func isStdio() bool {
	for _, arg := range os.Args[1:] {
		if arg == flagStdio {
			return true
		}
	}
	return false
}

// wantsStdio resolves the transport: an explicit Config.Transport wins; the
// --stdio os.Args scan is the backward-compat fallback for callers without a
// flag layer.
func wantsStdio(cfg Config) bool {
	return cfg.Transport == TransportStdio || isStdio()
}

// Serve creates a server from cfg (applying server-only options like KeepAlive
// and SchemaCache), lets register add tools/handlers, then runs it. It is the
// one-call alternative to the NewServer + Run pair: config lives in ONE place,
// so there is no dead/duplicated server-option field. Blocks until the server
// stops (same lifecycle as Run).
func Serve(impl *mcp.Implementation, cfg Config, register func(*mcp.Server)) error {
	server := NewServer(impl, cfg) // applies KeepAlive + SchemaCache at creation
	if register != nil {
		register(server)
	}
	return Run(server, cfg.withoutServerOpts())
}

// Run starts the MCP server and blocks until a signal is received.
// In stdio mode (--stdio flag), it runs via stdin/stdout.
// Otherwise, it starts an HTTP server with middleware, /mcp, /health, and optional /metrics.
//
//nolint:cyclop // server bootstrap fans out over transport modes (stdio/http/sse); complexity is inherent and stable
func Run(server *mcp.Server, cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	if !cfg.DisableMCP && server == nil {
		return errors.New("mcpserver: server must not be nil when DisableMCP is false")
	}
	cfg = withDefaults(cfg)
	stdio := wantsStdio(cfg)

	// Wire up REST bridge cleanup receiver — buildHandler will populate
	// restBridgeCleanup if RESTBridge is enabled, and we call it AFTER
	// srv.Shutdown() so in-flight requests drain first.
	var restBridgeCleanup func()
	cfg.onRESTBridgeCleanup = &restBridgeCleanup

	logger := cfg.Logger
	if logger == nil {
		w := os.Stdout
		if stdio {
			w = os.Stderr
		}
		logger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	slog.SetDefault(logger)
	warnIgnoredServerOpts(cfg, logger)

	applyMCPMiddleware(server, cfg)

	if stdio {
		logger.Info("running in stdio mode", slog.String("service", cfg.Name))
		ctx := cfg.Context
		if ctx == nil {
			ctx = context.Background()
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}

	var (
		sigCtx context.Context
		cancel context.CancelFunc
	)
	if cfg.Context != nil {
		sigCtx, cancel = context.WithCancel(cfg.Context)
	} else {
		sigCtx, cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	}
	defer cancel()

	// Pass a separate context to buildHandler — NOT sigCtx. The REST bridge
	// safety-net goroutine listens on this ctx; if we passed sigCtx, the
	// goroutine would close sessions immediately on signal, before
	// srv.Shutdown() drains in-flight HTTP requests. Using a non-cancellable
	// context ensures sessions stay open until the cleanup function is called
	// after srv.Shutdown(). The defer cancel() below is a safety net for
	// abnormal Run() exit (error paths).
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()

	h, err := buildHandler(bridgeCtx, server, cfg, logger)
	if err != nil {
		return fmt.Errorf("mcpserver: %w", err)
	}

	srv := &http.Server{
		Addr:         net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:      h,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	listenErr := make(chan error, 1)
	go func() {
		logger.Info("listening",
			slog.String("service", cfg.Name),
			slog.String("addr", srv.Addr),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
		}
	}()

	select {
	case <-sigCtx.Done():
		// normal shutdown path
	case err := <-listenErr:
		logger.Error("server failed", slog.Any("error", err))
		cancel()
		return err
	}
	logger.Info("shutting down", slog.String("service", cfg.Name))

	if cfg.OnShutdown != nil {
		cfg.OnShutdown()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.Any("error", err))
		return err
	}

	// Close REST bridge sessions AFTER the HTTP server has drained
	// in-flight requests — closing them earlier would cause mid-flight
	// REST tool calls to lose their session.
	if restBridgeCleanup != nil {
		restBridgeCleanup()
	}

	logger.Info("stopped", slog.String("service", cfg.Name))
	return nil
}

// Build returns an http.Handler with middleware, health, and optional /mcp routes.
// Use for testing or embedding in a custom server; use [Run] for production.
func Build(server *mcp.Server, cfg Config) (http.Handler, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	if !cfg.DisableMCP && server == nil {
		return nil, errors.New("mcpserver: server must not be nil when DisableMCP is false")
	}
	cfg = withDefaults(cfg)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	warnIgnoredServerOpts(cfg, logger)
	applyMCPMiddleware(server, cfg)
	return buildHandler(buildCtx(cfg), server, cfg, logger)
}

func buildHandler(ctx context.Context, server *mcp.Server, cfg Config, logger *slog.Logger) (http.Handler, error) {
	mux := http.NewServeMux()

	if !cfg.DisableMCP {
		stateless := cfg.Stateless == nil || *cfg.Stateless
		var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, &mcp.StreamableHTTPOptions{
			Stateless:                    stateless,
			SessionTimeout:               cfg.SessionTimeout,
			EventStore:                   cfg.EventStore,
			JSONResponse:                 cfg.JSONResponse,
			MaxRequestBodyBytes:          cfg.MaxRequestBodyBytes,
			PropagateRequestCancellation: cancelOnDisconnect(cfg),
			Logger:                       cfg.MCPLogger,
			DisableLocalhostProtection:   cfg.DisableLocalhostProtection,
		})

		// Only stateless handlers: a stateful session may resume a dropped
		// POST stream, so an aborted POST there is not necessarily abandoned.
		if stateless && cancelOnDisconnect(cfg) {
			mcpHandler = trackClientDisconnect(mcpHandler)
		}

		if cfg.BearerAuth != nil {
			mcpHandler = applyBearerAuth(mcpHandler, cfg.BearerAuth)
		}

		mux.Handle("/mcp", mcpHandler)
		mux.Handle("/mcp/", mcpHandler)
	}

	if cfg.BearerAuth != nil && cfg.BearerAuth.Metadata != nil {
		path := cfg.BearerAuth.ResourceMetadataPath
		if path == "" {
			path = "/.well-known/oauth-protected-resource"
		}
		mux.Handle("GET "+path, auth.ProtectedResourceMetadataHandler(cfg.BearerAuth.Metadata))
	}

	registerHealth(mux, cfg)

	if cfg.Metrics != nil {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(cfg.Metrics()))
		})
	}

	if cfg.Routes != nil {
		cfg.Routes(mux)
	}

	if cfg.RESTBridge && !cfg.DisableMCP {
		cleanup, err := startRESTBridge(ctx, server, mux, cfg, logger)
		if err != nil {
			return nil, fmt.Errorf("REST bridge init failed: %w", err)
		}
		// Store cleanup for Run() to call after srv.Shutdown().
		// For Build() (testing), the ctx.Done() safety net in startRESTBridge
		// handles cleanup when the test context is cancelled.
		if cfg.onRESTBridgeCleanup != nil {
			*cfg.onRESTBridgeCleanup = cleanup
		}
	}

	return Chain(mux, buildMiddleware(cfg, logger)...), nil
}

// warnLoopbackBypassOnce ensures the reverse-proxy warning fires at most once
// per process even when applyBearerAuth is invoked for both /mcp and the
// REST bridge prefix.
var warnLoopbackBypassOnce sync.Once

// applyBearerAuth wraps handler with bearer token verification.
// When LoopbackBypass is set, requests from 127.0.0.1/::1 skip auth.
//
// Emits a one-shot slog.Warn on first call when LoopbackBypass=true. The
// warning always fires (not just in containers) because a reverse proxy on
// bare metal has the same RemoteAddr=loopback effect.
func applyBearerAuth(handler http.Handler, cfg *BearerAuth) http.Handler {
	if cfg.LoopbackBypass {
		warnLoopbackBypassOnce.Do(func() {
			msg := "BearerAuth.LoopbackBypass=true — auth will be skipped for any request whose RemoteAddr is loopback. If a reverse proxy fronts this service, every external request looks like loopback and bearer auth is effectively DISABLED. Set LoopbackBypass=false unless you control the listener directly."
			if looksContainerised() {
				msg += " (containerised host detected — reverse proxy misconfiguration is the most common cause.)"
			}
			slog.Warn(msg)
		})
		if cfg.ToolFilter != nil && cfg.LoopbackIdentity == nil {
			warnLoopbackFilterOnce.Do(func() {
				slog.Warn("BearerAuth.ToolFilter is configured but LoopbackBypass requests carry no identity — ToolFilter receives nil TokenInfo for every bypassed caller and cannot distinguish them. Set BearerAuth.LoopbackIdentity to attach an explicit identity, or the filter silently decides on a nil identity.")
			})
		}
	}
	metaPath := cfg.ResourceMetadataPath
	if metaPath == "" && cfg.Metadata != nil {
		metaPath = "/.well-known/oauth-protected-resource"
	}
	authMW := auth.RequireBearerToken(cfg.Verifier,
		&auth.RequireBearerTokenOptions{
			ResourceMetadataURL: metaPath,
			Scopes:              cfg.Scopes,
		})
	authed := authMW(handler)
	if !cfg.LoopbackBypass {
		return authed
	}
	loopback := loopbackChain(handler, cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r) {
			loopback.ServeHTTP(w, r)
			return
		}
		authed.ServeHTTP(w, r)
	})
}

// warnLoopbackFilterOnce fires the "ToolFilter sees nil identity under
// LoopbackBypass" warning at most once per process.
var warnLoopbackFilterOnce sync.Once

// loopbackTokenBytes is the entropy length of loopbackSyntheticToken.
const loopbackTokenBytes = 32

// loopbackSyntheticToken is generated per process so nothing outside this
// code path can present it. Its only purpose is to route a bypassed
// request through auth.RequireBearerToken's context plumbing — the sole
// public way to populate RequestExtra.TokenInfo / auth.TokenInfoFromContext.
var loopbackSyntheticToken = func() string {
	b := make([]byte, loopbackTokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("mcpserver: generate loopback token: %v", err))
	}
	return "loopback-" + hex.EncodeToString(b)
}()

// loopbackChain wraps handler so LoopbackBypass-admitted requests run
// through the same TokenInfo plumbing as bearer-authenticated ones when
// cfg.LoopbackIdentity is set. Without it they pass through unauthenticated
// (TokenInfo nil) — the documented pre-LoopbackIdentity behaviour.
func loopbackChain(handler http.Handler, cfg *BearerAuth) http.Handler {
	if cfg.LoopbackIdentity == nil {
		return handler
	}
	verifier := func(_ context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(token), []byte(loopbackSyntheticToken)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		info, err := cfg.LoopbackIdentity(r)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, fmt.Errorf("%w: nil loopback identity", auth.ErrInvalidToken)
		}
		out := *info
		if out.Expiration.IsZero() {
			out.Expiration = time.Now().Add(time.Hour)
		}
		return &out, nil
	}
	authed := auth.RequireBearerToken(verifier,
		&auth.RequireBearerTokenOptions{Scopes: cfg.Scopes})(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.Header.Set("Authorization", "Bearer "+loopbackSyntheticToken)
		authed.ServeHTTP(w, r2)
	})
}

// looksContainerised reports whether this process appears to be running
// inside a Kubernetes pod or a Docker container. Used purely for surfacing
// a misconfiguration warning when LoopbackBypass is enabled in an
// environment where reverse proxies typically front the service.
func looksContainerised() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

// isLoopback returns true if the request originates from localhost.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// buildCtx returns the context from cfg, or context.Background() if nil.
func buildCtx(cfg Config) context.Context {
	if cfg.Context != nil {
		return cfg.Context
	}
	return context.Background()
}

// cancelOnDisconnect reports whether tool handlers should be cancelled when
// the client aborts the request (Config.CancelOnClientDisconnect, default true).
func cancelOnDisconnect(cfg Config) bool {
	return cfg.CancelOnClientDisconnect == nil || *cfg.CancelOnClientDisconnect
}
