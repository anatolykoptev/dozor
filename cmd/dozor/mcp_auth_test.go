package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

// buildAuthTestHandler builds the real /mcp handler stack from
// baseMCPConfig — the config `gateway` and `serve` both run — so dropping
// BearerAuth there, or bypassing baseMCPConfig, is what these tests catch.
func buildAuthTestHandler(t *testing.T) http.Handler {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "dozor-test", Version: "0"}, nil)
	h, err := mcpserver.Build(server, baseMCPConfig("", ""))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

func postInit(h http.Handler, auth string) int {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// RED-on-revert: delete BearerAuth from baseMCPConfig (mcp_auth.go) and the
// no-token and wrong-token cases return 200.
func TestMCPBearerAuth_TokenSet(t *testing.T) {
	t.Setenv(mcpTokenEnv, "s3cret-token")
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")
	h := buildAuthTestHandler(t)

	if got := postInit(h, ""); got != http.StatusUnauthorized {
		t.Errorf("no Authorization: status %d, want 401", got)
	}
	if got := postInit(h, "Bearer wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d, want 401", got)
	}
	if got := postInit(h, "Bearer s3cret-token"); got != http.StatusOK {
		t.Errorf("right token: status %d, want 200", got)
	}
}

// Fail closed: a missing token must not mean an open endpoint.
// RED-on-revert: return nil from mcpBearerAuth when the token is empty.
func TestMCPBearerAuth_TokenUnset_RejectsAll(t *testing.T) {
	t.Setenv(mcpTokenEnv, "")
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")
	h := buildAuthTestHandler(t)

	if got := postInit(h, ""); got != http.StatusUnauthorized {
		t.Errorf("no token configured, no header: status %d, want 401", got)
	}
	if got := postInit(h, "Bearer "); got != http.StatusUnauthorized {
		t.Errorf("no token configured, empty bearer: status %d, want 401", got)
	}
}

func TestMCPBearerAuth_InsecureOverride(t *testing.T) {
	t.Setenv(mcpTokenEnv, "")
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "true")
	h := buildAuthTestHandler(t)

	if got := postInit(h, ""); got != http.StatusOK {
		t.Errorf("explicit insecure override: status %d, want 200", got)
	}
}
