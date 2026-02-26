package session

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBinary       = "claude"
	defaultAllowedTools = "mcp__*,Read,Edit,Write,Bash,Glob,Grep,WebFetch,WebSearch,Task"
	defaultIdleTimeout  = 15 * time.Minute
)

// Config holds settings for interactive Claude Code sessions.
type Config struct {
	Binary       string
	MCPPort      string // empty = MCP disabled
	AllowedTools string
	IdleTimeout  time.Duration
}

// ConfigFromEnv reads session configuration from environment variables.
// Reuses existing DOZOR_CLAUDE_* vars plus DOZOR_SESSION_IDLE_TIMEOUT.
func ConfigFromEnv() Config {
	binary := strings.TrimSpace(os.Getenv("DOZOR_CLAUDE_BINARY"))
	if binary == "" {
		binary = defaultBinary
	}

	allowedTools := os.Getenv("DOZOR_CLAUDE_ALLOWED_TOOLS")
	if allowedTools == "" {
		allowedTools = defaultAllowedTools
	}

	var mcpPort string
	if os.Getenv("DOZOR_CLAUDE_MCP_ENABLED") != "false" {
		mcpPort = os.Getenv("DOZOR_MCP_PORT")
		if mcpPort == "" {
			mcpPort = "8765"
		}
	}

	idleTimeout := defaultIdleTimeout
	if s := os.Getenv("DOZOR_SESSION_IDLE_TIMEOUT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			idleTimeout = time.Duration(n) * time.Second
		}
	}

	return Config{
		Binary:       binary,
		MCPPort:      mcpPort,
		AllowedTools: allowedTools,
		IdleTimeout:  idleTimeout,
	}
}
