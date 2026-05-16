package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Marker regex tests
// ---------------------------------------------------------------------------

func TestNoDeployMarkerRE(t *testing.T) {
	matches := []string{
		"[no-deploy]",
		"[no-auto-deploy]",
		"[NO-DEPLOY]",
		"[NO-AUTO-DEPLOY]",
		"[no_deploy]",
		"[no_auto_deploy]",
		"[no-autodeploy]",
		"[NoAutoDeploy]",
		// marker anywhere in the message body
		"fix: update config\n\n[no-deploy]",
		"feat: add thing [no-auto-deploy] to skip",
	}
	for _, msg := range matches {
		if !noDeployMarkerRE.MatchString(msg) {
			t.Errorf("expected match for %q", msg)
		}
	}

	noMatches := []string{
		"",
		"normal commit message",
		"nodeploy",       // bare word, no brackets
		"no-deploy",      // no brackets
		"no auto deploy", // no brackets
		"[deploy]",
		"[auto-deploy]",
	}
	for _, msg := range noMatches {
		if noDeployMarkerRE.MatchString(msg) {
			t.Errorf("expected NO match for %q", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers for httptest-backed API tests
// ---------------------------------------------------------------------------

// prLabelsResponse is the minimal GitHub API response shape.
type prLabelsResponse []struct {
	Number int `json:"number"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// stubPRServer returns an httptest.Server that serves the given PR JSON body
// at /repos/*/commits/*/pulls.
func stubPRServer(t *testing.T, statusCode int, body prLabelsResponse) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		if statusCode != 200 {
			http.Error(w, "error", statusCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newCheckerWithBase creates a prLabelChecker pointing at a custom base URL.
func newCheckerWithBase(token, baseURL string) *prLabelChecker {
	c := newPRLabelChecker(token)
	c.apiBase = baseURL // test seam
	return c
}

// ---------------------------------------------------------------------------
// API stub tests — ShouldSkip via HTTP
// ---------------------------------------------------------------------------

func TestPRLabelChecker_NoPRs(t *testing.T) {
	srv, calls := stubPRServer(t, 200, prLabelsResponse{})
	c := newCheckerWithBase("tok", srv.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "abc123", "normal message")
	if got {
		t.Error("expected false (0 PRs)")
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("expected 1 API call, got %d", n)
	}
}

func TestPRLabelChecker_PRNoLabels(t *testing.T) {
	body := prLabelsResponse{{Number: 42, Labels: nil}}
	srv, _ := stubPRServer(t, 200, body)
	c := newCheckerWithBase("tok", srv.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "sha1", "normal message")
	if got {
		t.Error("expected false (PR with no labels)")
	}
}

func TestPRLabelChecker_PRWithNoAutoDeployLabel(t *testing.T) {
	body := prLabelsResponse{{
		Number: 7,
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "no-auto-deploy"}},
	}}
	srv, _ := stubPRServer(t, 200, body)
	c := newCheckerWithBase("tok", srv.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "sha2", "normal message")
	if !got {
		t.Error("expected true (PR has no-auto-deploy label)")
	}
}

func TestPRLabelChecker_PRWithOtherLabel(t *testing.T) {
	body := prLabelsResponse{{
		Number: 9,
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "enhancement"}, {Name: "ready"}},
	}}
	srv, _ := stubPRServer(t, 200, body)
	c := newCheckerWithBase("tok", srv.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "sha3", "normal message")
	if got {
		t.Error("expected false (PR has other labels only)")
	}
}

func TestPRLabelChecker_API500_FailOpen(t *testing.T) {
	srv, _ := stubPRServer(t, 500, nil)
	c := newCheckerWithBase("tok", srv.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "sha4", "normal message")
	if got {
		t.Error("expected false (fail-open on 500)")
	}
}

func TestPRLabelChecker_Timeout_FailOpen(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // much longer than prLabelAPITimeout
	}))
	defer slow.Close()
	c := newCheckerWithBase("tok", slow.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "sha5", "normal message")
	if got {
		t.Error("expected false (fail-open on timeout)")
	}
}

// ---------------------------------------------------------------------------
// Cache tests
// ---------------------------------------------------------------------------

func TestPRLabelChecker_CacheHit(t *testing.T) {
	body := prLabelsResponse{{
		Number: 1,
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "no-auto-deploy"}},
	}}
	srv, calls := stubPRServer(t, 200, body)
	c := newCheckerWithBase("tok", srv.URL)

	// First call: API hit
	first := c.ShouldSkip(context.Background(), "owner/repo", "sha6", "normal")
	if !first {
		t.Fatal("expected true on first call")
	}
	// Second call: same SHA → cache hit, no additional API call
	second := c.ShouldSkip(context.Background(), "owner/repo", "sha6", "normal")
	if !second {
		t.Error("expected true on second call (from cache)")
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("expected exactly 1 API call total, got %d", n)
	}
}

func TestPRLabelChecker_LRUEviction(t *testing.T) {
	srv, calls := stubPRServer(t, 200, prLabelsResponse{})
	c := newCheckerWithBase("tok", srv.URL)

	// Store prLabelCacheSize+1 distinct SHAs — the first one should be evicted.
	for i := 0; i < prLabelCacheSize+1; i++ {
		sha := "sha-fill-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26))
		c.ShouldSkip(context.Background(), "owner/repo", sha, "normal")
	}

	initialCalls := atomic.LoadInt64(calls)

	// Now re-query the very first SHA: if evicted, it triggers another API call.
	c.ShouldSkip(context.Background(), "owner/repo", "sha-fill-a-0", "normal")
	newCalls := atomic.LoadInt64(calls) - initialCalls
	if newCalls == 0 {
		t.Error("expected at least 1 new API call after first SHA was evicted")
	}
}

// ---------------------------------------------------------------------------
// Empty token — marker works, API not called
// ---------------------------------------------------------------------------

func TestPRLabelChecker_EmptyToken_MarkerWorks(t *testing.T) {
	var apiCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&apiCalls, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newCheckerWithBase("", srv.URL)

	// Marker in commit message → skip without API
	got := c.ShouldSkip(context.Background(), "owner/repo", "sha7", "[no-deploy] emergency stop")
	if !got {
		t.Error("expected true from marker even with empty token")
	}
	if n := atomic.LoadInt64(&apiCalls); n != 0 {
		t.Errorf("expected 0 API calls with empty token, got %d", n)
	}
}

func TestPRLabelChecker_EmptyToken_NoMarker_APISkipped(t *testing.T) {
	var apiCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&apiCalls, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[{"number":1,"labels":[{"name":"no-auto-deploy"}]}]`))
	}))
	defer srv.Close()

	c := newCheckerWithBase("", srv.URL)

	// No marker, no token → should NOT call API, return false
	got := c.ShouldSkip(context.Background(), "owner/repo", "sha8", "normal commit")
	if got {
		t.Error("expected false (empty token, no marker)")
	}
	if n := atomic.LoadInt64(&apiCalls); n != 0 {
		t.Errorf("expected 0 API calls with empty token and no marker, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Marker fast-path — no API call even with valid token
// ---------------------------------------------------------------------------

func TestPRLabelChecker_MarkerFastPath_NoAPICalled(t *testing.T) {
	var apiCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&apiCalls, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newCheckerWithBase("valid-token", srv.URL)

	got := c.ShouldSkip(context.Background(), "owner/repo", "sha9", "[no-auto-deploy] skip me")
	if !got {
		t.Error("expected true from marker")
	}
	if n := atomic.LoadInt64(&apiCalls); n != 0 {
		t.Errorf("expected 0 API calls on marker fast-path, got %d", n)
	}
}
