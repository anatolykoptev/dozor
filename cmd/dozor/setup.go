package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/dozor/internal/agent"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/skills"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/dozor/pkg/extensions"
	"github.com/anatolykoptev/dozor/pkg/extensions/a2aclient"
	"github.com/anatolykoptev/dozor/pkg/extensions/claudecode"
	"github.com/anatolykoptev/dozor/pkg/extensions/mcpclient"
	"github.com/anatolykoptev/dozor/pkg/extensions/websearch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// agentStack holds all components needed to run the LLM agent loop.
type agentStack struct {
	registry      *toolreg.Registry
	skillsLoader  *skills.Loader
	llm           provider.Provider
	loop          *agent.Loop
	workspacePath string
}

// buildAgentStack creates the full LLM agent stack (tool registry, skills, LLM, loop).
// Used by gateway and smart-watch commands.
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
	loop := agent.NewLoop(llm, registry, llm.MaxIterations(), workspacePath, skillsLoader)

	return &agentStack{
		registry:      registry,
		skillsLoader:  skillsLoader,
		llm:           llm,
		loop:          loop,
		workspacePath: workspacePath,
	}
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
func buildMCPServer(eng *engine.ServerAgent) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dozor",
		Version: version,
	}, nil)
	tools.RegisterAll(server, eng)
	return server
}

// buildExtensionRegistry creates and loads the extension registry.
// gatewayMode=true adds mcpclient and a2aclient extensions.
func buildExtensionRegistry(eng *engine.ServerAgent, registry *toolreg.Registry, mcpServer *mcp.Server, gatewayMode bool) *extensions.Registry {
	extRegistry := extensions.NewRegistry()
	extRegistry.Register(websearch.New())
	extRegistry.Register(claudecode.New())
	if gatewayMode {
		extRegistry.Register(mcpclient.New())
		extRegistry.Register(a2aclient.New())
	}
	extRegistry.LoadAll(context.Background(), eng, registry, mcpServer)
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
	srv.Shutdown(shutdownCtx) //nolint:errcheck
	slog.Info(label + " stopped")
}
