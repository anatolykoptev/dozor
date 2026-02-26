package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runServe starts the MCP server in HTTP or stdio mode.
func runServe(cfg engine.Config, eng *engine.ServerAgent) {
	defer eng.Close()
	stdio := hasFlag("--stdio")

	logWriter := os.Stdout
	if stdio {
		logWriter = os.Stderr
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo})))

	server := buildMCPServer(eng, tools.ExecOptions{Config: tools.NewExecConfig()})
	buildExtensionRegistry(eng, nil, server, false, nil)

	slog.Info("dozor MCP server",
		slog.String("mode", map[bool]string{true: "stdio", false: "http"}[stdio]))

	if stdio {
		slog.Info("running in stdio mode")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			slog.Error("stdio server failed", slog.Any("error", err))
			eng.Close()
			os.Exit(1) //nolint:gocritic // explicit cleanup called before os.Exit
		}
		return
	}

	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	mx := http.NewServeMux()
	mx.Handle("/mcp", buildMCPHTTPHandler(server))
	mx.Handle("/mcp/", buildMCPHTTPHandler(server))
	mx.HandleFunc("GET /health", healthHandler(""))

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	startHTTPServer(sigCtx, &http.Server{
		Addr:         ":" + port,
		Handler:      mx,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}, "MCP server")
}
