package deploy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// autoSignSecret is the secret newTestHandler injects when a test config has
// none. The webhook is fail-closed (issue #256): an empty secret rejects every
// request, so tests that exercise dispatch/filter logic rather than auth go
// through this wrapper, which signs their requests transparently.
const autoSignSecret = "test-autosign-secret"

// signingHandler wraps Handler and signs requests that carry no signature
// header. Tests that set a signature header themselves (wrong or right) are
// passed through untouched, and configs with an explicit secret are never
// auto-signed.
type signingHandler struct {
	*Handler
	autoSign bool
}

func newTestHandler(cfg *Config, q *Queue, notify func(string)) *signingHandler {
	auto := cfg.Secret == "" || cfg.Secret == autoSignSecret // a config may be reused across handlers
	if auto {
		cfg.Secret = autoSignSecret
	}
	return &signingHandler{Handler: NewHandler(cfg, q, notify), autoSign: auto}
}

func (s *signingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.autoSign && r.Header.Get("X-Hub-Signature-256") == "" && r.Header.Get("X-Hub-Signature") == "" {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.Header.Set("X-Hub-Signature-256", computeSignature(string(body), autoSignSecret))
	}
	s.Handler.ServeHTTP(w, r)
}

// An empty secret must disable the endpoint (503), never skip authentication.
// Regression guard for issue #256: previously every request was accepted.
func TestHandler_EmptySecret_RejectsEverything(t *testing.T) {
	t.Parallel()

	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
	cases := []struct {
		name  string
		event string
		sig   string
	}{
		{"unsigned push", "push", ""},
		{"unsigned ping", "ping", ""},
		{"garbage signature", "push", "sha256=deadbeef"},
		{"signature of empty secret", "push", computeSignature(body, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig("") // Secret == ""
			q, _ := newTestQueue()
			h := NewHandler(cfg, q, func(string) {})
			t.Cleanup(h.Close)

			req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
			req.Header.Set("X-GitHub-Event", tc.event)
			if tc.sig != "" {
				req.Header.Set("X-Hub-Signature-256", tc.sig)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			if !strings.Contains(w.Body.String(), "no secret configured") {
				t.Errorf("body = %q, want it to mention the missing secret", w.Body.String())
			}
			q.mu.Lock()
			n := len(q.pending)
			q.mu.Unlock()
			if n != 0 {
				t.Errorf("%d build(s) enqueued by a rejected request", n)
			}
		})
	}
}

func TestHandler_EmptySecret_CountsRejection(t *testing.T) {
	// Not parallel: asserts on a process-global counter delta.
	before := testutil.ToFloat64(WebhookRejectedTotal.WithLabelValues("no_secret"))
	h := NewHandler(testConfig(""), &Queue{}, func(string) {})
	t.Cleanup(h.Close)

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader("{}"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := testutil.ToFloat64(WebhookRejectedTotal.WithLabelValues("no_secret")) - before; got != 1 {
		t.Errorf("no_secret rejections = %v, want 1", got)
	}
}

// With a secret configured, behaviour is unchanged: unsigned and badly signed
// requests get 401, a correctly signed one is processed.
func TestHandler_WithSecret_AuthUnchanged(t *testing.T) {
	t.Parallel()

	const secret = "s3cret"
	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
	cases := []struct {
		name string
		sig  string
		want int
	}{
		{"unsigned", "", http.StatusUnauthorized},
		{"wrong secret", computeSignature(body, "other"), http.StatusUnauthorized},
		{"valid", computeSignature(body, secret), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, _ := newTestQueue()
			h := NewHandler(testConfig(secret), q, func(string) {})
			t.Cleanup(h.Close)

			req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
			req.Header.Set("X-GitHub-Event", "push")
			if tc.sig != "" {
				req.Header.Set("X-Hub-Signature-256", tc.sig)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
