package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// WebFetch is the fallback for server_web_fetch, whose URL comes from the
// MCP caller. RED-on-revert: replace httputil.NewSSRFGuardedClient in
// WebFetch with a plain *http.Client and the loopback fetch succeeds.
func TestWebFetch_RefusesLoopback(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		_, _ = w.Write([]byte("internal"))
	}))
	defer srv.Close()

	_, err := WebFetch(context.Background(), srv.URL, 100)
	if err == nil {
		t.Fatal("WebFetch fetched a loopback URL; want refusal")
	}
	if !strings.HasPrefix(err.Error(), "refused:") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error %q: want the generic refusal without the address", err)
	}
	if hit.Load() {
		t.Fatal("loopback server was reached")
	}
}
