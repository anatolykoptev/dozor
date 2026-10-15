package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/dozor/internal/identity"
	"github.com/anatolykoptev/go-kit/tracing/httpmw"
	"github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The identity listener is a second, loopback-only MCP endpoint that accepts
// key-file keys only and serves the agent tools only. It is optional and it
// is never allowed to affect the operator listener: every failure here logs
// ERROR, sets dozor_identity_listener_up to 0 and returns.
const (
	identityPortEnv = "DOZOR_IDENTITY_PORT"
	keysFileEnv     = "DOZOR_MCP_KEYS_FILE"

	// identityBindHost is a constant, not configuration: this listener must
	// never be reachable from outside the host.
	identityBindHost = "127.0.0.1"

	identityMaxBody = 6 << 20 // agentproto.MaxRequestBytes plus JSON-RPC envelope slack
	// An agent_exec call may run for MaxTimeoutS plus the client grace, which
	// stays under the edge's 300 s idle limit; the other tools are bounded by
	// the client's own 30 s deadline.
	identityExecTimeout = (agentproto.MaxTimeoutS * time.Second) + agentproto.ExecGrace + selfCheckTimeout
	identityToolTimeout = 45 * time.Second
	identityWriteWindow = identityExecTimeout + selfCheckTimeout

	selfCheckTimeout = 5 * time.Second
	shutdownGrace    = 10 * time.Second
)

// identityListenAddr is the only address the identity listener binds.
func identityListenAddr(port string) string {
	return net.JoinHostPort(identityBindHost, port)
}

// identityConfig is read from the environment; the listener starts only when
// both values are set.
type identityConfig struct {
	port     string
	keysFile string
}

func identityConfigFromEnv() (identityConfig, bool) {
	c := identityConfig{port: os.Getenv(identityPortEnv), keysFile: os.Getenv(keysFileEnv)}
	return c, c.port != "" && c.keysFile != ""
}

// operatorToolNames lists the tools a server registers, over an in-memory
// session (no auth layer, so it sees every registered tool).
func operatorToolNames(ctx context.Context, srv *mcp.Server) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, selfCheckTimeout)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "dozor-selfcheck", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(lt.Tools))
	for _, t := range lt.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

// assertDisjoint refuses to run the identity listener if any agent tool name
// is also an operator tool name: "disallowed" must mean "not registered".
func assertDisjoint(agent, operator []string) error {
	var both []string
	for _, n := range agent {
		if slices.Contains(operator, n) {
			both = append(both, n)
		}
	}
	if len(both) > 0 {
		return fmt.Errorf("agent tools %v are also operator tools", both)
	}
	return nil
}

// buildIdentityHandler builds the identity MCP endpoint: one server that
// registers only the agent tools, authenticated by the key file alone. The
// master token and every other credential are 401 here, because the only
// verifier is the key file's.
func buildIdentityHandler(kf *keyfile.Store, ccfg agentsurface.Config, operatorTools []string) (http.Handler, error) {
	server := mcpserver.NewServer(&mcp.Implementation{Name: "dozor-identity", Version: version},
		mcpserver.Config{SchemaCache: mcp.NewSchemaCache()})
	// Innermost (see buildMCPServer); Audit is added after Build, below.
	server.AddReceivingMiddleware(identity.Recover(slog.Default()))
	identity.ClientHooks(&ccfg, slog.Default())
	agentsurface.Register(server, identity.RunAsResolver, ccfg)

	cfg := mcpserver.Config{
		Name:                       "dozor-identity",
		Version:                    version,
		DisableHealth:              true, // no non-MCP route is served here
		DisableLocalhostProtection: true, // the edge proxy forwards the public Host header
		JSONResponse:               true,
		MaxRequestBodyBytes:        identityMaxBody,
		ToolTimeout:                identityToolTimeout,
		ToolTimeouts:               map[string]time.Duration{agentsurface.ToolExec: identityExecTimeout},
		Logger:                     slog.Default(),
		MCPLogger:                  slog.Default(),
	}
	if err := kf.Install(&cfg); err != nil {
		return nil, err
	}
	cfg.BearerAuth.ToolFilter = identity.Ceiling(cfg.BearerAuth.ToolFilter)
	// A method-level refusal by the key file (non-tool methods) is audited too.
	for i := range cfg.MCPReceivingMiddleware {
		cfg.MCPReceivingMiddleware[i] = identity.MarkRefusals(cfg.MCPReceivingMiddleware[i])
	}
	h, err := mcpserver.Build(server, cfg)
	if err != nil {
		return nil, err
	}
	// After Build, so it is the outermost layer: every call, including ones
	// the filter denies, is recorded once. Tool labels are clamped to the
	// registered names of both servers, so a made-up name has no series.
	server.AddReceivingMiddleware(identity.Audit(slog.Default(),
		identity.WithKnownTools(agentsurface.ToolNames, operatorTools)))
	return h, nil
}

// startAuxHTTPServer binds srv.Addr and serves it in the background until ctx
// is done. Unlike startHTTPServer it is non-fatal: a bind or serve error is
// logged at ERROR, up is set to 0, and the process keeps running.
func startAuxHTTPServer(ctx context.Context, srv *http.Server, label string, up interface{ Set(float64) }) (net.Addr, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		slog.Error(label+" cannot listen; continuing without it", slog.String("addr", srv.Addr), slog.Any("error", err))
		up.Set(0)
		return nil, err
	}
	up.Set(1)
	slog.Info(label+" listening", slog.String("addr", ln.Addr().String()))
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error(label+" stopped on an error", slog.Any("error", err))
			up.Set(0)
		}
	}()
	go func() { //nolint:gosec // ctx is already cancelled by the time the drain runs; it needs its own deadline
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("HTTP server shutdown error", slog.String("server", label), slog.Any("error", err))
		}
		up.Set(0)
		slog.Info(label + " stopped")
	}()
	return ln.Addr(), nil
}

// startIdentityListener starts the identity listener when DOZOR_IDENTITY_PORT
// and DOZOR_MCP_KEYS_FILE are both set. It returns the bound address, or an
// error that has already been logged. It never panics and never exits the
// process; operatorPort is the port the operator listener owns.
func startIdentityListener(ctx context.Context, cfg identityConfig, operator *mcp.Server, operatorPort string, ccfg agentsurface.Config) (addr net.Addr, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("identity listener setup panicked; continuing without it", slog.Any("panic", r))
			identity.ListenerUp.Set(0)
			addr, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	fail := func(msg string, err error) (net.Addr, error) {
		slog.Error("identity listener not started: "+msg, slog.Any("error", err))
		identity.ListenerUp.Set(0)
		return nil, err
	}

	port, perr := strconv.ParseUint(cfg.port, 10, 16)
	if perr != nil {
		return fail("invalid "+identityPortEnv, perr)
	}
	if op, oerr := strconv.ParseUint(operatorPort, 10, 16); oerr == nil && op == port {
		return fail("port clashes with the operator listener", errors.New(identityPortEnv+" equals the operator port"))
	}
	names, err := operatorToolNames(ctx, operator)
	if err != nil {
		return fail("cannot list operator tools for the disjointness check", err)
	}
	if err := assertDisjoint(agentsurface.ToolNames, names); err != nil {
		return fail("tool sets overlap", err)
	}
	kf, err := keyfile.Open(identity.KeyfileOptions(cfg.keysFile, slog.Default()))
	if err != nil {
		return fail("cannot open the key file", err)
	}
	identity.ObserveKeyfile(kf)
	h, err := buildIdentityHandler(kf, ccfg, names)
	if err != nil {
		return fail("cannot build the handler", err)
	}
	// Same recovery and request middleware as the operator listener.
	srv := &http.Server{
		Addr:         identityListenAddr(cfg.port),
		Handler:      httpmw.Handler("dozor", recoveryMiddleware(h)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: identityWriteWindow,
	}
	addr, err = startAuxHTTPServer(ctx, srv, "identity listener", identity.ListenerUp)
	return addr, err
}

// maybeStartIdentityListener is what the gateway calls: it starts the
// identity listener if (and only if) it is configured. Failures are logged
// and never returned to the caller's control flow.
func maybeStartIdentityListener(ctx context.Context, operator *mcp.Server, operatorPort string) (net.Addr, error) {
	cfg, ok := identityConfigFromEnv()
	if !ok {
		return nil, nil //nolint:nilnil // not configured is not an error
	}
	return startIdentityListener(ctx, cfg, operator, operatorPort, agentsurface.Config{})
}
