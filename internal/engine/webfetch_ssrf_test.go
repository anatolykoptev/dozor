package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// WebFetch is the fallback for server_web_fetch, whose URL comes from the
// MCP caller. RED-on-revert: replace httputil.NewSSRFGuardedClient in
// WebFetch with a plain *http.Client and the loopback fetch succeeds.
func TestWebFetch_RefusesLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte("internal"))
	}))
	defer srv.Close()

	if _, err := WebFetch(context.Background(), srv.URL, 100); err == nil {
		t.Fatal("WebFetch fetched a loopback URL; want refusal")
	}
	if hit {
		t.Fatal("loopback server was reached")
	}
}
