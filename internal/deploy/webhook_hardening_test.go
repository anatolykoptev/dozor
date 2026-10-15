package deploy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Issue #262: the GitHub signature must be the "sha256=" envelope. A bare hex
// MAC (slice-by-length instead of CutPrefix) or a sha1= envelope must not pass.
func TestVerifySignature_RequiresSha256Prefix(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"test": true}`)
	const secret = "0123456789abcdef-secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	hexMAC := hex.EncodeToString(mac.Sum(nil))

	cases := []struct {
		name string
		sig  string
		want bool
	}{
		{"canonical", "sha256=" + hexMAC, true},
		{"bare hex, no prefix", hexMAC, false},
		{"bare hex padded to prefix length", "xxxxxxx" + hexMAC, false},
		{"sha1 envelope with sha256 MAC", "sha1=" + hexMAC, false},
		{"uppercase prefix", "SHA256=" + hexMAC, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := verifySignature(payload, tc.sig, secret); got != tc.want {
				t.Errorf("verifySignature(%q) = %v, want %v", tc.sig, got, tc.want)
			}
		})
	}
}

// Auth must hold for EVERY event type, not just push: a code path that skips
// verification for one event (e.g. release) must turn this red.
func TestHandler_WithSecret_AuthTable_AllEventTypes(t *testing.T) {
	t.Parallel()

	const secret = "0123456789abcdef-secret"
	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
	bare := strings.TrimPrefix(computeSignature(body, secret), "sha256=")

	events := []string{"push", "release", "ping", "workflow_run"} // last = unknown event
	sigs := []struct {
		name    string
		headers map[string]string
		wantOK  bool
	}{
		{"unsigned", nil, false},
		{"wrong secret", map[string]string{"X-Hub-Signature-256": computeSignature(body, "other-secret-0123456789")}, false},
		{"bare hex without prefix", map[string]string{"X-Hub-Signature-256": bare}, false},
		{"valid MAC via legacy header only", map[string]string{"X-Hub-Signature": computeSignature(body, secret)}, false},
		{"valid", map[string]string{"X-Hub-Signature-256": computeSignature(body, secret)}, true},
	}
	for _, ev := range events {
		for _, sc := range sigs {
			t.Run(ev+"/"+sc.name, func(t *testing.T) {
				t.Parallel()
				q, _ := newTestQueue()
				h := NewHandler(testConfig(secret), q, func(string) {})
				t.Cleanup(h.Close)

				req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
				req.Header.Set("X-GitHub-Event", ev)
				for k, v := range sc.headers {
					req.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)

				if sc.wantOK {
					if w.Code == http.StatusUnauthorized || w.Code == http.StatusServiceUnavailable {
						t.Fatalf("event %q: valid signature rejected with %d", ev, w.Code)
					}
					return
				}
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("event %q, %s: status = %d, want 401", ev, sc.name, w.Code)
				}
				q.mu.Lock()
				n := len(q.pending)
				q.mu.Unlock()
				if n != 0 {
					t.Errorf("event %q: %d build(s) enqueued by an unauthenticated request", ev, n)
				}
			})
		}
	}
}

// syncBuffer lets the slog handler and the test goroutine share a buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureDeploySlog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// Behind Caddy RemoteAddr is always 127.0.0.1, so the rejection log must carry
// the client IP resolved by the injected trusted-proxy logic - and must never
// echo the attacker-supplied signature header values (#262, #267 review).
func TestHandler_RejectionLog_ClientIPAndNoSignatureEcho(t *testing.T) {
	// Not parallel: swaps the process-global slog default.
	logs := captureDeploySlog(t)
	const secret = "0123456789abcdef-secret"
	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")

	q, _ := newTestQueue()
	h := NewHandler(testConfig(secret), q, func(string) {})
	t.Cleanup(h.Close)
	h.ClientIP = func(*http.Request) string { return "203.0.113.7" }

	for _, hdr := range []map[string]string{
		{"X-Hub-Signature-256": "sha256=deadbeefcafe0badc0ffee"},
		{"X-Hub-Signature": "sha1=feedfacef00dbaadbeef"},
		nil, // missing
	} {
		req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set("X-GitHub-Event", "push")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	}

	out := logs.String()
	if !strings.Contains(out, "203.0.113.7") {
		t.Errorf("rejection log lacks the resolved client IP:\n%s", out)
	}
	if strings.Contains(out, "127.0.0.1") {
		t.Errorf("rejection log still carries the proxy address:\n%s", out)
	}
	for _, leaked := range []string{"deadbeefcafe0badc0ffee", "feedfacef00dbaadbeef"} {
		if strings.Contains(out, leaked) {
			t.Errorf("rejection log echoes attacker-supplied signature %q:\n%s", leaked, out)
		}
	}
}

func TestHandler_ClientIP_DefaultsToRemoteHost(t *testing.T) {
	logs := captureDeploySlog(t)
	q, _ := newTestQueue()
	h := NewHandler(testConfig("0123456789abcdef-secret"), q, func(string) {})
	t.Cleanup(h.Close)

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader("{}"))
	req.RemoteAddr = "198.51.100.9:4242"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(logs.String(), "198.51.100.9") {
		t.Errorf("default client IP not logged:\n%s", logs.String())
	}
}

const minimalDeployYAML = `
repos:
  anatolykoptev/dozor:
    compose_path: /home/krolik/deploy/krolik-server
    source_path: /home/krolik/src/dozor
    services: [dozor]
`

// A secret under 16 bytes is treated as unset: Secret is empty, so the handler
// answers 503 (fail-closed) instead of authenticating with a guessable key.
func TestLoadConfig_ShortSecret_FailsClosed(t *testing.T) {
	cases := []struct {
		name, env, wantSecret string
	}{
		{"short", "short", ""},
		{"15 bytes", strings.Repeat("a", 15), ""},
		{"whitespace pads a short secret", "  " + strings.Repeat("a", 10) + "  \n", ""},
		{"exactly 16 bytes", strings.Repeat("a", 16), strings.Repeat("a", 16)},
		{"trimmed", "  " + strings.Repeat("b", 64) + "\n", strings.Repeat("b", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", tc.env)
			cfg, err := LoadConfig(writeYAML(t, t.TempDir(), minimalDeployYAML))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Secret != tc.wantSecret {
				t.Fatalf("Secret = %q, want %q", cfg.Secret, tc.wantSecret)
			}

			q, _ := newTestQueue()
			h := NewHandler(cfg, q, func(string) {})
			t.Cleanup(h.Close)
			req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader("{}"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			want := http.StatusUnauthorized
			if tc.wantSecret == "" {
				want = http.StatusServiceUnavailable
			}
			if w.Code != want {
				t.Errorf("status = %d, want %d", w.Code, want)
			}
		})
	}
}

// GitHub's published example (docs: "Validating webhook deliveries"). An oracle
// not derived from our own computeSignature helper.
func TestVerifySignature_GitHubPublishedVector(t *testing.T) {
	t.Parallel()
	const (
		secret  = "It's a Secret to Everybody"
		payload = "Hello, World!"
		want    = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	)
	if !verifySignature([]byte(payload), want, secret) {
		t.Error("GitHub's documented signature did not verify")
	}
	if computeSignature(payload, secret) != want {
		t.Errorf("computeSignature = %s, want %s", computeSignature(payload, secret), want)
	}
}

// A body over the cap is answered 413 (counted), never HMAC-ed truncated - even
// when the signature covers exactly the first maxWebhookBody bytes.
func TestHandler_OversizedBody_413AndCounted(t *testing.T) {
	// Not parallel: asserts on a process-global counter delta.
	const secret = "0123456789abcdef-secret"
	before := testutil.ToFloat64(WebhookRejectedTotal.WithLabelValues("body_too_large"))
	q, _ := newTestQueue()
	h := NewHandler(testConfig(secret), q, func(string) {})
	t.Cleanup(h.Close)

	large := strings.Repeat("x", maxWebhookBody+1)
	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(large))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", computeSignature(large[:maxWebhookBody], secret))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if got := testutil.ToFloat64(WebhookRejectedTotal.WithLabelValues("body_too_large")) - before; got != 1 {
		t.Errorf("body_too_large rejections = %v, want 1", got)
	}
}

// Defence in depth: a Config built directly (not via LoadConfig) with a short
// secret must still fail closed.
func TestNewHandler_ShortSecret_FailsClosed(t *testing.T) {
	t.Parallel()
	for _, secret := range []string{"short", "  " + strings.Repeat("a", 10) + "\n"} {
		q, _ := newTestQueue()
		h := NewHandler(&Config{Secret: secret, Repos: testConfig("").Repos}, q, func(string) {})
		t.Cleanup(h.Close)
		body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
		req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Hub-Signature-256", computeSignature(body, strings.TrimSpace(secret)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("secret %q: status = %d, want 503", secret, w.Code)
		}
	}
}

// The "too short" ERROR belongs to the gateway path (NewHandler, once), not to
// every LoadConfig call made by the MCP tools.
func TestLoadConfig_ShortSecret_DoesNotLog(t *testing.T) {
	logs := captureDeploySlog(t)
	t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", "short")
	if _, err := LoadConfig(writeYAML(t, t.TempDir(), minimalDeployYAML)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "DOZOR_GITHUB_WEBHOOK_SECRET") {
		t.Errorf("LoadConfig logged about the secret:\n%s", logs.String())
	}
}
