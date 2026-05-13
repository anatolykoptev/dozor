package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
)

var version = "dev"

func main() {
	loadDotenv(".env")

	cfg := engine.Init()
	eng := engine.NewAgent(cfg)

	// Bootstrap dev mode from env. When DOZOR_DEV_MODE is truthy at startup,
	// the periodic watch becomes observe-only: triage runs and notifications
	// fire as usual, but auto-remediation is gated off and the routing prompt
	// instructs the LLM not to take corrective action. Survives restarts as
	// long as the env var stays set — unlike the runtime `server_dev_mode`
	// toggle which resets on restart.
	if isTruthy(os.Getenv("DOZOR_DEV_MODE")) {
		eng.SetDevMode(true)
		slog.Info("dev mode enabled at startup", slog.String("source", "DOZOR_DEV_MODE env"))
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(cfg, eng)
	case "gateway":
		runGateway(cfg, eng)
	case "check":
		runCheck(cfg, eng)
	case "watch":
		runWatch(cfg, eng)
	default:
		printUsage()
		os.Exit(1)
	}
}

// isTruthy returns true for "1", "true", "yes", "on" (case-insensitive).
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `dozor - server monitoring agent

Usage:
  dozor serve [--port PORT] [--stdio]        MCP server (HTTP or stdio)
  dozor gateway [--port PORT]                Full agent: MCP + A2A + Telegram
  dozor check [--json] [--services s1,s2]    One-shot diagnostics
  dozor watch [--interval 4h] [--smart]      Periodic monitoring daemon
`)
}
