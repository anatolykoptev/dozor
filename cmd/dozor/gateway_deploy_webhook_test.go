package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// readTracker records whether the request body was ever read.
type readTracker struct {
	r    io.Reader
	read *atomic.Bool
}

func (t readTracker) Read(p []byte) (int, error) {
	t.read.Store(true)
	return t.r.Read(p)
}

var deployTestSrc atomic.Int32

func deployTestAddr() string {
	n := deployTestSrc.Add(1)
	return fmt.Sprintf("10.77.%d.%d:5000", n/250, n%250+1)
}

// registerRealDeployWebhook runs the production registerDeployWebhook (the same
// function runGateway calls) against a temp deploy config and returns the mux.
func registerRealDeployWebhook(t *testing.T) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DOZOR_WORKSPACE", dir)
	t.Setenv("DOZOR_GITHUB_TOKEN", "")
	t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", strings.Repeat("s", 32))
	cfg := "repos:\n  test/ratelimit-repo:\n    compose_path: /tmp\n    source_path: /tmp\n    services: [ratelimit-svc]\n"
	if err := os.WriteFile(filepath.Join(dir, "deploy-repos.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mx := http.NewServeMux()
	registerDeployWebhook(ctx, mx, func(string) {}, func([]engine.Alert, string) {})
	return mx
}

// Issue #262: the public /deploy/github route must be rate-limited BEFORE the
// body is read and the HMAC computed. The test drives the handler that the
// production registration puts on the mux, not the limiter in isolation.
func TestRegisterDeployWebhook_RateLimitsBeforeBodyRead(t *testing.T) {
	// Not parallel: asserts on a process-global counter delta.
	mx := registerRealDeployWebhook(t)
	before := testutil.ToFloat64(deploy.WebhookRejectedTotal.WithLabelValues("rate_limited"))
	addr := deployTestAddr()
	post := func(addr string) (*httptest.ResponseRecorder, *atomic.Bool) {
		var read atomic.Bool
		req := httptest.NewRequest(http.MethodPost, "/deploy/github",
			readTracker{r: strings.NewReader("{}"), read: &read})
		req.RemoteAddr = addr
		req.Header.Set("X-GitHub-Event", "push")
		rr := httptest.NewRecorder()
		mx.ServeHTTP(rr, req)
		return rr, &read
	}

	// A full burst (every repo merging at once) reaches the real handler, which
	// rejects the unsigned requests with 401 - not 429.
	for i := 0; i < deployWebhookBurst; i++ {
		if rr, _ := post(addr); rr.Code != http.StatusUnauthorized {
			t.Fatalf("request %d of burst %d: status = %d, want 401 (GitHub burst starved?)", i+1, deployWebhookBurst, rr.Code)
		}
	}
	// Beyond the burst: 429 and the body is never touched.
	rr, read := post(addr)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("over-burst status = %d, want 429", rr.Code)
	}
	if read.Load() {
		t.Error("request body was read before the rate-limit decision")
	}
	if got := testutil.ToFloat64(deploy.WebhookRejectedTotal.WithLabelValues("rate_limited")) - before; got != 1 {
		t.Errorf("rate_limited rejections = %v, want 1", got)
	}
	// Another source is unaffected.
	if rr, _ := post(deployTestAddr()); rr.Code != http.StatusUnauthorized {
		t.Errorf("a fresh source got %d, want 401 (reached the handler)", rr.Code)
	}
}

// The rejection log must carry the trusted-proxy client IP, not the Caddy hop:
// registerDeployWebhook injects webhookSourceKey into the handler.
func TestRegisterDeployWebhook_RejectionLogsForwardedClientIP(t *testing.T) {
	mx := registerRealDeployWebhook(t)
	prevTrust := trustProxy
	trustProxy = true
	t.Cleanup(func() { trustProxy = prevTrust })

	var buf syncBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:" + fmt.Sprint(40000+deployTestSrc.Add(1))
	req.Header.Set("X-Forwarded-For", "203.0.113.77")
	rr := httptest.NewRecorder()
	mx.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if !strings.Contains(buf.String(), "203.0.113.77") {
		t.Errorf("rejection log lacks the forwarded client IP:\n%s", buf.String())
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
