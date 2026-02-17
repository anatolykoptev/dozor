package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var agent *engine.ServerAgent

func main() {
	// Load .env
	loadDotenv(".env")

	cfg := engine.Init()
	agent = engine.NewAgent(cfg)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(cfg)
	case "check":
		runCheck(cfg)
	case "watch":
		runWatch(cfg)
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `dozor - server monitoring agent

Usage:
  dozor serve [--port PORT] [--stdio]    MCP server (HTTP or stdio)
  dozor check [--json] [--services s1,s2] One-shot diagnostics
  dozor watch [--interval 4h]            Periodic monitoring daemon
`)
}

func runServe(cfg engine.Config) {
	stdio := hasFlag("--stdio")

	logWriter := os.Stdout
	if stdio {
		logWriter = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dozor",
		Version: "1.0.0",
	}, nil)

	registerTools(server)
	logger.Info("dozor MCP server", slog.Int("tools", 8))

	if stdio {
		logger.Info("running in stdio mode")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			logger.Error("stdio server failed", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}

	// HTTP mode
	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mx := http.NewServeMux()
	mx.Handle("/mcp", handler)
	mx.Handle("/mcp/", handler)
	mx.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"dozor","version":"1.0.0"}`))
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mx,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-sigCtx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
	logger.Info("stopped")
}

func runCheck(cfg engine.Config) {
	ctx := context.Background()
	asJSON := hasFlag("--json")

	// Override services if specified
	var services []string
	if s := getFlagValue("--services"); s != "" {
		services = strings.Split(s, ",")
	}

	report := agent.Diagnose(ctx, services)

	if asJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(engine.FormatReport(report))
	}

	if report.NeedsAttention() {
		os.Exit(1)
	}
}

func runWatch(cfg engine.Config) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if i := getFlagValue("--interval"); i != "" {
		if d, err := time.ParseDuration(i); err == nil {
			cfg.WatchInterval = d
		}
	}
	if u := getFlagValue("--webhook"); u != "" {
		cfg.WebhookURL = u
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := engine.Watch(sigCtx, cfg); err != nil {
		logger.Error("watch failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// hasFlag checks if a flag exists in os.Args.
func hasFlag(flag string) bool {
	for _, a := range os.Args[2:] {
		if a == flag {
			return true
		}
	}
	return false
}

// getFlagValue returns the value after a flag (--flag value).
func getFlagValue(flag string) string {
	args := os.Args[2:]
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		// Handle --flag=value
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}
