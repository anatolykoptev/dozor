package mcpserver

import (
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewTestServer creates an [httptest.Server] from [Build] and registers
// cleanup via t.Cleanup. Useful for integration tests without starting
// a real HTTP server.
func NewTestServer(t testing.TB, server *mcp.Server, cfg Config) *httptest.Server {
	t.Helper()
	h, err := Build(server, cfg)
	if err != nil {
		t.Fatalf("mcpserver.Build: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}
