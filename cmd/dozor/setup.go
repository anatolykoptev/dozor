package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/agent"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/mcpclient"
	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/skills"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/dozor/pkg/extensions"
	"github.com/anatolykoptev/dozor/pkg/extensions/a2aclient"
	"github.com/anatolykoptev/dozor/pkg/extensions/claudecode"
	extmcpclient "github.com/anatolykoptev/dozor/pkg/extensions/mcpclient"
	"github.com/anatolykoptev/dozor/pkg/extensions/websearch"
	session "github.com/anatolykoptev/go-session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// agentStack holds all components needed to run the LLM agent loop.
//
// The loop field is populated lazily by attachLoop so it can be constructed
// after the extension registry loads and expose the MemDB KBSearcher for the
// Phase 6.3 startup snapshot. Call sites that don't need a snapshot (e.g.
// smart-watch) pass nil.
type agentStack struct {
	registry      *toolreg.Registry
	skillsLoader  *skills.Loader
	llm           provider.Provider
	maxIters      int
	loop          *agent.Loop
	sessions      *agent.SessionStore
	workspacePath string
}

// buildAgentStack creates the tool registry, skills loader, LLM provider
// and session store. It does NOT create the agent loop — the loop needs
// the extension-provided KBSearcher for the Phase 6.3 startup snapshot, so
// call attachLoop after buildExtensionRegistry to finish wiring the stack.
func buildAgentStack(eng *engine.ServerAgent) *agentStack {
	workspacePath := resolveWorkspacePath()

	builtinSkillsDir := resolveBuiltinDir("skills")
	defaultsDir := resolveBuiltinDir("workspace")
	skills.InitWorkspace(workspacePath, defaultsDir)

	registry := toolreg.NewRegistry()
	toolreg.RegisterAll(registry, eng)

	skillsLoader := skills.NewLoader(workspacePath+"/skills", builtinSkillsDir)
	skills.RegisterTools(registry, skillsLoader)
	skills.RegisterMemoryTools(registry, workspacePath)

	slog.Info("tool registry initialized",
		slog.Int("tools", len(registry.List())),
		slog.Int("skills", len(skillsLoader.ListSkills())))

	llm := provider.NewFromEnv()
	maxIters := llm.MaxIterations()

	sessionsDir := workspacePath + "/sessions"
	sessions := agent.NewSessionStore(sessionsDir)
	sessions.WithCompactor(buildSummarizeFn(llm))

	return &agentStack{
		registry:      registry,
		skillsLoader:  skillsLoader,
		llm:           llm,
		maxIters:      maxIters,
		sessions:      sessions,
		workspacePath: workspacePath,
	}
}

// attachLoop builds the agent loop on top of an existing stack. The
// searcher parameter is forwarded to agent.NewLoop for the Phase 6.3
// startup snapshot; pass nil when MemDB isn't configured or when the
// caller doesn't want a snapshot (e.g. smart-watch).
func (s *agentStack) attachLoop(searcher *mcpclient.KBSearcher) {
	loop := agent.NewLoop(s.llm, s.registry, s.maxIters, s.workspacePath, s.skillsLoader, searcher)
	loop.WithSessions(s.sessions)
	s.loop = loop
}

// kbSearcherFromExtensions retrieves the programmatic KBSearcher from the
// mcpclient extension, or returns nil if the extension isn't registered or
// MemDB isn't configured.
func kbSearcherFromExtensions(extRegistry *extensions.Registry) *mcpclient.KBSearcher {
	ext := extRegistry.Get("mcpclient")
	if ext == nil {
		return nil
	}
	typed, ok := ext.(*extmcpclient.MCPClientExtension)
	if !ok {
		return nil
	}
	return typed.KBSearcher()
}

// resolveWorkspacePath returns the DOZOR_WORKSPACE path or ~/.dozor.
func resolveWorkspacePath() string {
	if p := os.Getenv("DOZOR_WORKSPACE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return home + "/.dozor"
}

// buildMCPServer creates an MCP server and registers all core tools.
func buildMCPServer(eng *engine.ServerAgent, execOpts tools.ExecOptions) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dozor",
		Version: version,
	}, nil)
	tools.RegisterAllWithOpts(server, eng, execOpts)
	return server
}

// buildExtensionRegistry creates and loads the extension registry.
// gatewayMode=true adds mcpclient and a2aclient extensions.
func buildExtensionRegistry(eng *engine.ServerAgent, registry *toolreg.Registry, mcpServer *mcp.Server, gatewayMode bool, notify func(string)) *extensions.Registry {
	extRegistry := extensions.NewRegistry()
	extRegistry.Register(websearch.New())
	extRegistry.Register(claudecode.New())
	if gatewayMode {
		extRegistry.Register(extmcpclient.New())
		extRegistry.Register(a2aclient.New())
	}
	if err := extRegistry.LoadAll(context.Background(), eng, registry, mcpServer, notify); err != nil {
		slog.Warn("extension registry load error", slog.Any("error", err))
	}
	extRegistry.RegisterIntrospectTool(mcpServer)

	for _, extErr := range extRegistry.Errors() {
		slog.Warn("extension error",
			slog.String("ext", extErr.Extension),
			slog.String("phase", string(extErr.Phase)),
			slog.Any("error", extErr.Err))
	}
	slog.Info("extensions loaded", slog.Int("count", len(extRegistry.List())))
	return extRegistry
}

// buildMCPHTTPHandler returns a streamable HTTP handler for the given MCP server.
func buildMCPHTTPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

// healthHandler returns a simple JSON health endpoint handler.
func healthHandler(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if mode != "" {
			fmt.Fprintf(w, `{"status":"ok","service":"dozor","version":"%s","mode":"%s"}`, version, mode)
		} else {
			fmt.Fprintf(w, `{"status":"ok","service":"dozor","version":"%s"}`, version)
		}
	}
}

// startHTTPServer runs srv in a goroutine and shuts it down when ctx is done.
// Returns after shutdown completes.
func startHTTPServer(ctx context.Context, srv *http.Server, label string) {
	go func() {
		slog.Info(label+" listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error(label+" failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down " + label)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("HTTP server shutdown error", slog.String("server", label), slog.Any("error", err))
	}
	slog.Info(label + " stopped")
}

// buildSummarizeFn creates a session.SummarizeFn backed by the LLM provider.
func buildSummarizeFn(p provider.Provider) session.SummarizeFn {
	return func(_ context.Context, prompt string) (string, error) {
		resp, err := p.Chat(
			[]provider.Message{
				{Role: "system", Content: "You are a concise conversation summarizer. Output only the summary, no preamble."},
				{Role: "user", Content: prompt},
			},
			nil,
		)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(resp.Content), nil
	}
}
