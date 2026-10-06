package session

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"
)

// The interactive session's MCP config must carry dozor's /mcp token, or the
// session silently runs without dozor's tools once /mcp requires it, and the
// temp file holding the token must be removable by the returned cleanup.
// RED-on-revert: write the dozor entry without mcpself.ServerEntry, or drop
// the returned cleanup.
func TestBuildCLIArgs_MCPConfigCarriesTokenAndIsCleanedUp(t *testing.T) {
	t.Setenv("DOZOR_MCP_TOKEN", "sess-tok")
	t.Setenv("HOME", t.TempDir()) // no ~/.mcp.json to merge

	args, cleanup := buildCLIArgs(Config{MCPPort: "8765"})
	var path string
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			path = args[i+1]
		}
	}
	if path == "" {
		t.Fatalf("no --mcp-config in args %v", args)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse MCP config: %v", err)
	}
	d := cfg.MCPServers["dozor"]
	if d.URL != "http://127.0.0.1:8765/mcp" || d.Headers["Authorization"] != "Bearer sess-tok" {
		t.Fatalf("dozor entry = %+v, want url + Authorization: Bearer sess-tok", d)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("MCP config perms = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}

	cleanup()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MCP config still present after cleanup (err %v)", err)
	}
}
