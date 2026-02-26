package session

import (
	"testing"
	"time"
)

// TestConfigFromEnv_Defaults verifies that ConfigFromEnv returns expected defaults
// when no environment variables are set.
func TestConfigFromEnv_Defaults(t *testing.T) {
	// Clear all relevant env vars.
	t.Setenv("DOZOR_CLAUDE_BINARY", "")
	t.Setenv("DOZOR_CLAUDE_ALLOWED_TOOLS", "")
	t.Setenv("DOZOR_CLAUDE_MCP_ENABLED", "")
	t.Setenv("DOZOR_MCP_PORT", "")
	t.Setenv("DOZOR_SESSION_IDLE_TIMEOUT", "")

	cfg := ConfigFromEnv()

	if cfg.Binary != defaultBinary {
		t.Errorf("Binary = %q, want %q", cfg.Binary, defaultBinary)
	}
	if cfg.AllowedTools != defaultAllowedTools {
		t.Errorf("AllowedTools = %q, want %q", cfg.AllowedTools, defaultAllowedTools)
	}
	if cfg.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, defaultIdleTimeout)
	}
	// MCP is enabled by default; port falls back to "8765".
	if cfg.MCPPort != "8765" {
		t.Errorf("MCPPort = %q, want %q", cfg.MCPPort, "8765")
	}
}

// TestConfigFromEnv_Custom verifies that each env var is correctly picked up.
func TestConfigFromEnv_Custom(t *testing.T) {
	t.Setenv("DOZOR_CLAUDE_BINARY", "/usr/local/bin/claude-custom")
	t.Setenv("DOZOR_CLAUDE_ALLOWED_TOOLS", "Bash,Read")
	t.Setenv("DOZOR_CLAUDE_MCP_ENABLED", "true")
	t.Setenv("DOZOR_MCP_PORT", "9999")
	t.Setenv("DOZOR_SESSION_IDLE_TIMEOUT", "300") // 300 seconds = 5 minutes

	cfg := ConfigFromEnv()

	if cfg.Binary != "/usr/local/bin/claude-custom" {
		t.Errorf("Binary = %q, want %q", cfg.Binary, "/usr/local/bin/claude-custom")
	}
	if cfg.AllowedTools != "Bash,Read" {
		t.Errorf("AllowedTools = %q, want %q", cfg.AllowedTools, "Bash,Read")
	}
	if cfg.MCPPort != "9999" {
		t.Errorf("MCPPort = %q, want %q", cfg.MCPPort, "9999")
	}
	want := 300 * time.Second
	if cfg.IdleTimeout != want {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, want)
	}
}

// TestConfigFromEnv_MCPDisabled verifies that setting DOZOR_CLAUDE_MCP_ENABLED=false
// disables MCP (MCPPort = "").
func TestConfigFromEnv_MCPDisabled(t *testing.T) {
	t.Setenv("DOZOR_CLAUDE_MCP_ENABLED", "false")
	t.Setenv("DOZOR_MCP_PORT", "9999") // should be ignored when MCP is disabled

	cfg := ConfigFromEnv()

	if cfg.MCPPort != "" {
		t.Errorf("MCPPort = %q, want empty string when MCP is disabled", cfg.MCPPort)
	}
}

// TestConfigFromEnv_MCPCustomPort verifies that DOZOR_MCP_PORT overrides the default.
func TestConfigFromEnv_MCPCustomPort(t *testing.T) {
	t.Setenv("DOZOR_CLAUDE_MCP_ENABLED", "") // not "false" → MCP enabled
	t.Setenv("DOZOR_MCP_PORT", "12345")

	cfg := ConfigFromEnv()

	if cfg.MCPPort != "12345" {
		t.Errorf("MCPPort = %q, want %q", cfg.MCPPort, "12345")
	}
}

// TestConfigFromEnv_IdleTimeoutInvalid verifies that a non-numeric or non-positive
// DOZOR_SESSION_IDLE_TIMEOUT falls back to the default.
func TestConfigFromEnv_IdleTimeoutInvalid(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"non-numeric", "abc"},
		{"zero", "0"},
		{"negative", "-60"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOZOR_SESSION_IDLE_TIMEOUT", tc.val)

			cfg := ConfigFromEnv()

			if cfg.IdleTimeout != defaultIdleTimeout {
				t.Errorf("IdleTimeout = %v, want default %v for input %q",
					cfg.IdleTimeout, defaultIdleTimeout, tc.val)
			}
		})
	}
}

// TestConfigFromEnv_BinaryTrimSpace verifies that leading/trailing whitespace in
// DOZOR_CLAUDE_BINARY is stripped.
func TestConfigFromEnv_BinaryTrimSpace(t *testing.T) {
	t.Setenv("DOZOR_CLAUDE_BINARY", "  /my/claude  ")

	cfg := ConfigFromEnv()

	if cfg.Binary != "/my/claude" {
		t.Errorf("Binary = %q, want %q (trimmed)", cfg.Binary, "/my/claude")
	}
}

// TestConfigFromEnv_BinaryWhitespaceOnly verifies that a whitespace-only binary
// value falls back to the default binary.
func TestConfigFromEnv_BinaryWhitespaceOnly(t *testing.T) {
	t.Setenv("DOZOR_CLAUDE_BINARY", "   ")

	cfg := ConfigFromEnv()

	if cfg.Binary != defaultBinary {
		t.Errorf("Binary = %q, want default %q for whitespace-only input", cfg.Binary, defaultBinary)
	}
}
