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

// buildAuthTestHandler builds the real /mcp handler stack with the same
// BearerAuth the gateway passes, so the test exercises go-mcpserver's
// wiring rather than the verifier alone.
func buildAuthTestHandler(t *testing.T) http.Handler {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "dozor-test", Version: "0"}, nil)
	h, err := mcpserver.Build(server, mcpserver.Config{
		Name:                       "dozor-test",
		Version:                    "0",
		DisableLocalhostProtection: true,
		JSONResponse:               true,
		BearerAuth:                 mcpBearerAuth(),
	})
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

// Pins mcpBearerAuth against go-mcpserver's real handler stack. The
// gateway.go / serve.go call sites are covered by the post-deploy probe
// (POST /mcp without a token must return 401), not by this test.
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
