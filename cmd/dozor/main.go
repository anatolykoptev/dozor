package main

import (
	"fmt"
	"os"

	"github.com/anatolykoptev/dozor/internal/engine"
)

var version = "dev"

func main() {
	loadDotenv(".env")

	cfg := engine.Init()
	eng := engine.NewAgent(cfg)

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

func printUsage() {
	fmt.Fprintf(os.Stderr, `dozor - server monitoring agent

Usage:
  dozor serve [--port PORT] [--stdio]        MCP server (HTTP or stdio)
  dozor gateway [--port PORT]                Full agent: MCP + A2A + Telegram
  dozor check [--json] [--services s1,s2]    One-shot diagnostics
  dozor watch [--interval 4h] [--smart]      Periodic monitoring daemon
`)
}
