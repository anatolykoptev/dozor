package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/go-mcpserver"
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

	bindHost := resolveBindHost()
	if !isLoopbackBind(bindHost) {
		slog.Warn("MCP server bound to non-loopback interface — network-reachable",
			slog.String("bind_host", bindHost),
			slog.String("hint", "set DOZOR_BIND_HOST=127.0.0.1 for loopback-only binding"))
	}

	if err := mcpserver.Run(server, mcpserver.Config{
		Name:                       "dozor",
		Version:                    version,
		Port:                       port,
		KeepAlive:                  30 * time.Second,
		SchemaCache:                mcp.NewSchemaCache(),
		DisableLocalhostProtection: true,
		Logger:                     slog.Default(),
		MCPLogger:                  slog.Default(),
		JSONResponse:               true,
	}); err != nil {
		slog.Error("MCP server failed", slog.Any("error", err))
	}
}
