package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	kitratelimit "github.com/anatolykoptev/go-kit/ratelimit"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const deployTestSecret = "0123456789abcdef0123456789abcdef"

// testDeployBurst is the burst of the limiter the tests install. The refill
// rate is negligible so the outcome never depends on wall-clock time.
const testDeployBurst = 5

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

// deployTestAddr returns a fresh non-loopback peer address.
func deployTestAddr() string {
	n := deployTestSrc.Add(1)
	return fmt.Sprintf("10.77.%d.%d:5000", n/250, n%250+1)
}

// useTestDeployLimiter swaps the package limiter for one with a negligible
// refill rate and restores it afterwards.
func useTestDeployLimiter(t *testing.T) {
	t.Helper()
	prev := deployWebhookLimiter
	deployWebhookLimiter = kitratelimit.NewKeyLimiter(0.000001, testDeployBurst)
	t.Cleanup(func() {
		deployWebhookLimiter.Close()
		deployWebhookLimiter = prev
	})
}

// registerRealDeployWebhook runs the production registerDeployWebhook (the same
// function runGateway calls) against a temp deploy config and returns the mux.
// HOME is redirected so nothing under the real ~/.dozor is read or written.
func registerRealDeployWebhook(t *testing.T) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOZOR_WORKSPACE", dir)
	t.Setenv("DOZOR_GITHUB_TOKEN", "")
	t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", deployTestSecret)
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

// postDeploy sends one POST /deploy/github. sign=true adds a valid signature.
func postDeploy(mx *http.ServeMux, peer, xff string, sign bool) (*httptest.ResponseRecorder, *atomic.Bool) {
	var read atomic.Bool
	const body = "{}"
	req := httptest.NewRequest(http.MethodPost, "/deploy/github",
		readTracker{r: strings.NewReader(body), read: &read})
	req.RemoteAddr = peer
	req.Header.Set("X-GitHub-Event", "ping")
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if sign {
		mac := hmac.New(sha256.New, []byte(deployTestSecret))
		mac.Write([]byte(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	rr := httptest.NewRecorder()
	mx.ServeHTTP(rr, req)
	return rr, &read
}

// Issue #262: the public /deploy/github route must be rate-limited BEFORE the
// body is read and the HMAC computed. The test drives the handler that the
// production registration puts on the mux, not the limiter in isolation.
func TestRegisterDeployWebhook_RateLimitsBeforeBodyRead(t *testing.T) {
	// Not parallel: asserts on a process-global counter delta.
	useTestDeployLimiter(t)
	mx := registerRealDeployWebhook(t)
	before := testutil.ToFloat64(deploy.WebhookRejectedTotal.WithLabelValues("rate_limited"))
	addr := deployTestAddr()

	// The burst reaches the real handler, which rejects the unsigned request
	// with 401 - not 429.
	for i := 0; i < testDeployBurst; i++ {
		if rr, _ := postDeploy(mx, addr, "", false); rr.Code != http.StatusUnauthorized {
			t.Fatalf("request %d of burst %d: status = %d, want 401", i+1, testDeployBurst, rr.Code)
		}
	}
	// Beyond the burst: 429 and the body is never touched.
	rr, read := postDeploy(mx, addr, "", false)
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
	if rr, _ := postDeploy(mx, deployTestAddr(), "", false); rr.Code != http.StatusUnauthorized {
		t.Errorf("a fresh source got %d, want 401 (reached the handler)", rr.Code)
	}
}

// The production burst must cover ~20 repos delivering push+release at once.
func TestDeployWebhookBurst_CoversGitHubFanOut(t *testing.T) {
	t.Parallel()
	if deployWebhookBurst < 40 {
		t.Errorf("deployWebhookBurst = %d, want >= 40 (20 repos x push+release)", deployWebhookBurst)
	}
}

// Prod shape: Caddy connects from 127.0.0.1 and sets X-Forwarded-For to the real
// client. One client flooding must not exhaust GitHub's bucket (the pre-auth
// limiter used to be a single global bucket keyed on 127.0.0.1).
func TestRegisterDeployWebhook_PerClientBucketsBehindLoopbackProxy(t *testing.T) {
	useTestDeployLimiter(t)
	mx := registerRealDeployWebhook(t)
	const proxy = "127.0.0.1:41000"

	for i := 0; i < testDeployBurst; i++ {
		postDeploy(mx, proxy, "198.51.100.10", false) // flooding client A
	}
	if rr, _ := postDeploy(mx, proxy, "198.51.100.10", false); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("client A past burst: status = %d, want 429", rr.Code)
	}
	// Client B (GitHub) through the same proxy peer, correctly signed.
	rr, _ := postDeploy(mx, proxy, "192.0.2.50", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("signed request from client B: status = %d, want 200 (starved by A's flood)", rr.Code)
	}
}

// A non-loopback peer (e.g. a docker bridge client hitting :8765 directly) is
// keyed on its own address: a rotating X-Forwarded-For buys no fresh buckets.
func TestRegisterDeployWebhook_NonLoopbackPeerCannotRotateXFF(t *testing.T) {
	useTestDeployLimiter(t)
	mx := registerRealDeployWebhook(t)
	const peer = "172.17.0.5:50000"

	for i := 0; i < testDeployBurst; i++ {
		postDeploy(mx, peer, fmt.Sprintf("203.0.113.%d", i+1), false)
	}
	if rr, _ := postDeploy(mx, peer, "203.0.113.200", false); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("rotating-XFF request past burst: status = %d, want 429", rr.Code)
	}
}

// Rate-limited floods must not produce one log line per request.
func TestRegisterDeployWebhook_RateLimitedLogIsSampled(t *testing.T) {
	useTestDeployLimiter(t)
	mx := registerRealDeployWebhook(t)
	var buf syncBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	addr := deployTestAddr()
	for i := 0; i < testDeployBurst+30; i++ {
		postDeploy(mx, addr, "", false)
	}
	if n := strings.Count(buf.String(), "rate limited"); n > 1 {
		t.Errorf("%d 'rate limited' log lines for 30 limited requests, want <= 1", n)
	}
}

// The rejection log must carry the forwarded client IP, not the Caddy hop:
// registerDeployWebhook injects webhookSourceKey into the handler.
func TestRegisterDeployWebhook_RejectionLogsForwardedClientIP(t *testing.T) {
	mx := registerRealDeployWebhook(t)
	var buf syncBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rr, _ := postDeploy(mx, "127.0.0.1:"+fmt.Sprint(40000+deployTestSrc.Add(1)), "203.0.113.77", false)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if !strings.Contains(buf.String(), "203.0.113.77") {
		t.Errorf("rejection log lacks the forwarded client IP:\n%s", buf.String())
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
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
