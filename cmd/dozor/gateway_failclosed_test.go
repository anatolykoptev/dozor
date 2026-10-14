package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/docker/docker/api/types/container"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newSignedWebhookMux registers the REAL production routes via
// registerWebhookHandler — the same function runGateway calls — so these tests
// exercise routing + auth wiring, not a re-implemented helper. A mutant that
// bypasses the check at the registration site is caught here, not in a
// unit-level call to checkWebhookSignature.
//
// Returns the mux plus counters for the two downstream sinks a webhook post
// can reach: the agent-loop bus (PublishInbound) and the alert renderer
// (notifyAlertFn).
func newSignedWebhookMux(t *testing.T, secret, allowInsecure string) (*http.ServeMux, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	t.Setenv("DOZOR_WEBHOOK_SECRET", secret)
	t.Setenv("DOZOR_WEBHOOK_ALLOW_INSECURE", allowInsecure)

	msgBus := bus.New()
	var inboundCount, alertCount atomic.Int32
	notifyAlertFn := func([]engine.Alert, string) { alertCount.Add(1) }

	mx := http.NewServeMux()
	registerWebhookHandler(mx, msgBus, func(string) {}, notifyAlertFn)

	consumeCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, ok := msgBus.ConsumeInbound(consumeCtx)
			if !ok {
				return
			}
			inboundCount.Add(1)
		}
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		msgBus.Close()
	})
	return mx, &inboundCount, &alertCount
}

// postWebhook fires one unsigned POST against the mux. RemoteAddr is varied
// per call so the shared package-level webhookLimiter can't leak a 429 into
// a test that is asserting on auth behaviour.
var webhookTestSrc atomic.Int32

func postWebhook(mx *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = fmt.Sprintf("10.99.0.%d:4040", webhookTestSrc.Add(1)%250+1)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mx.ServeHTTP(rr, req)
	return rr
}

// TestWebhookRoutes_EmptySecret_FailClosed — T1. With DOZOR_WEBHOOK_SECRET
// unset and no override, every signed /webhook* route must answer 503
// "webhook disabled: no secret configured", increment
// dozor_webhook_rejected_total{reason="no_secret"}, and never reach a
// downstream sink (agent-loop bus or alert renderer).
func TestWebhookRoutes_EmptySecret_FailClosed(t *testing.T) {
	const body = `{"message":"external monitor alert"}`
	// Every path that runs checkWebhookSignature in production:
	//   /webhook                    — exact route (legacy agent loop + classifier)
	//   /webhook/                   — trailing-slash prefix route
	//   /webhook/go-code            — arbitrary sub-path, same prefix handler
	//   /webhook/monitor/healthcheck — explicit route via monitorHealthcheckHandler
	for _, path := range []string{"/webhook", "/webhook/", "/webhook/go-code", "/webhook/monitor/healthcheck"} {
		t.Run("path="+path, func(t *testing.T) {
			mx, inbound, alerts := newSignedWebhookMux(t, "", "")
			before := testutil.ToFloat64(webhookRejectedTotal.WithLabelValues("no_secret"))

			rr := postWebhook(mx, path, body)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("empty secret: want 503 for %s, got %d body=%s", path, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "webhook disabled: no secret configured") {
				t.Errorf("body = %q, want %q", rr.Body.String(), "webhook disabled: no secret configured")
			}
			time.Sleep(20 * time.Millisecond)
			if got := inbound.Load(); got != 0 {
				t.Errorf("downstream agent loop reached despite 503: PublishInbound=%d", got)
			}
			if got := alerts.Load(); got != 0 {
				t.Errorf("downstream alert renderer reached despite 503: notifyAlertFn=%d", got)
			}
			if got := testutil.ToFloat64(webhookRejectedTotal.WithLabelValues("no_secret")) - before; got != 1 {
				t.Errorf("no_secret rejection counter delta = %v, want 1", got)
			}
		})
	}
}

// TestWebhookRoutes_WhitespaceSecret_FailClosed — T2. A whitespace-only secret
// counts as empty: strings.TrimSpace before the emptiness check.
func TestWebhookRoutes_WhitespaceSecret_FailClosed(t *testing.T) {
	mx, inbound, alerts := newSignedWebhookMux(t, "   ", "")
	rr := postWebhook(mx, "/webhook", `{"message":"x"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("whitespace secret: want 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	if inbound.Load() != 0 || alerts.Load() != 0 {
		t.Errorf("downstream reached despite whitespace-secret 503 (inbound=%d alerts=%d)", inbound.Load(), alerts.Load())
	}
}

// TestWebhookRoutes_ValidSignature_ReachesDownstream — T3. Secret set: a valid
// HMAC still flows to the downstream handler (legacy no-event body → the
// agent-loop bus), and a bad signature still gets 401.
func TestWebhookRoutes_ValidSignature_ReachesDownstream(t *testing.T) {
	const secret = "fc-secret"
	const body = `{"message":"signed external alert"}`
	mx, inbound, _ := newSignedWebhookMux(t, secret, "")

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.RemoteAddr = "10.98.1.1:4040"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dozor-Webhook-Signature", signBody(secret, body))
	rr := httptest.NewRecorder()
	mx.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid sig: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	if got := inbound.Load(); got != 1 {
		t.Errorf("valid signed legacy post: want PublishInbound=1, got %d", got)
	}

	// Bad signature is still a rejection, and is counted.
	before := testutil.ToFloat64(webhookRejectedTotal.WithLabelValues("invalid_signature"))
	req2 := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req2.RemoteAddr = "10.98.1.2:4040"
	req2.Header.Set("X-Dozor-Webhook-Signature", "badc0ffee")
	rr2 := httptest.NewRecorder()
	mx.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig: want 401, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if got := testutil.ToFloat64(webhookRejectedTotal.WithLabelValues("invalid_signature")) - before; got != 1 {
		t.Errorf("invalid_signature counter delta = %v, want 1", got)
	}
}

// TestWebhookRoutes_AllowInsecure_RestoresOpenMode — T4. The explicit dev
// override DOZOR_WEBHOOK_ALLOW_INSECURE=true restores the pre-fix open
// behaviour on every signed /webhook* route.
func TestWebhookRoutes_AllowInsecure_RestoresOpenMode(t *testing.T) {
	const body = `{"message":"unsigned but allowed"}`
	for _, path := range []string{"/webhook", "/webhook/monitor/healthcheck"} {
		t.Run("path="+path, func(t *testing.T) {
			mx, inbound, alerts := newSignedWebhookMux(t, "", "true")
			rr := postWebhook(mx, path, body)
			if rr.Code != http.StatusOK {
				t.Fatalf("allow-insecure override: want 200 for %s, got %d body=%s", path, rr.Code, rr.Body.String())
			}
			time.Sleep(20 * time.Millisecond)
			if got := inbound.Load() + alerts.Load(); got == 0 {
				t.Errorf("override mode: no downstream sink reached for %s (inbound=%d alerts=%d)", path, inbound.Load(), alerts.Load())
			}
		})
	}
}

// TestAlertmanager_EmptySecret_BehaviourUnchanged — T6. This task must NOT
// change POST /webhook/alertmanager: it is governed by
// DOZOR_ALERTMANAGER_REQUIRE_SIG (issue #234) because stock Alertmanager
// cannot send an HMAC. Both halves pin today's behaviour exactly.
func TestAlertmanager_EmptySecret_BehaviourUnchanged(t *testing.T) {
	body := `{"version":"4","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},` +
		`"alerts":[{"status":"firing","labels":{"alertname":"Pin"},"annotations":{},` +
		`"startsAt":"2026-05-06T10:00:00Z","endsAt":"0001-01-01T00:00:00Z"}]}`

	// Default (REQUIRE_SIG unset): unsigned payload accepted, alert dispatched.
	t.Run("require_sig_unset_accepts_unsigned", func(t *testing.T) {
		spy, received := captureAlerts(t)
		handler := makeAlertmanagerHandler(t, "", "", spy)
		req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("want 204 (unchanged default), got %d body=%s", rr.Code, rr.Body.String())
		}
		if n := len(received()); n != 1 {
			t.Errorf("want 1 dispatched alert (unchanged default), got %d", n)
		}
	})

	// REQUIRE_SIG=true with an EMPTY secret still accepts unsigned today
	// (checkWebhookSignature returns true on empty secret). That is the
	// residual fail-open hole tracked by issue #234 — pinned here so this
	// task provably did not alter the alertmanager path.
	t.Run("require_sig_true_empty_secret_still_open", func(t *testing.T) {
		spy, received := captureAlerts(t)
		handler := makeAlertmanagerHandler(t, "", "true", spy)
		req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("want 204 (pinned pre-#234 behaviour), got %d body=%s", rr.Code, rr.Body.String())
		}
		if n := len(received()); n != 1 {
			t.Errorf("want 1 dispatched alert (pinned pre-#234 behaviour), got %d", n)
		}
	})
}

// TestLogsHandler_EmptyToken_FailClosed — T5 (fail-closed half). With
// DOZOR_API_TOKEN unset (or whitespace) and no override, GET /api/logs answers
// 503 "api disabled: no token configured" and the docker client is never
// touched.
func TestLogsHandler_EmptyToken_FailClosed(t *testing.T) {
	for _, token := range []string{"", "   "} {
		name := "empty"
		if token != "" {
			name = "whitespace"
		}
		t.Run(name, func(t *testing.T) {
			var listCalls atomic.Int32
			fake := &fakeContainerLogger{
				containers: []container.Summary{makeContainer("id1", "svc", "svc")},
				listCalls:  &listCalls,
			}
			t.Setenv("DOZOR_API_TOKEN", token)
			t.Setenv("DOZOR_API_ALLOW_INSECURE", "")
			mx := http.NewServeMux()
			registerLogsHandler(mx, fake)
			srv := httptest.NewServer(mx)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/logs?service=svc")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("token %q: want 503, got %d", token, resp.StatusCode)
			}
			buf := make([]byte, 256)
			n, _ := resp.Body.Read(buf)
			if !strings.Contains(string(buf[:n]), "api disabled: no token configured") {
				t.Errorf("body = %q, want %q", string(buf[:n]), "api disabled: no token configured")
			}
			if got := listCalls.Load(); got != 0 {
				t.Errorf("docker ContainerList reached despite 503: calls=%d", got)
			}
		})
	}
}

// TestLogsHandler_AllowInsecure_RestoresOpenMode — T5 (override half). The
// explicit dev override DOZOR_API_ALLOW_INSECURE=true restores the pre-fix
// unauthenticated behaviour.
func TestLogsHandler_AllowInsecure_RestoresOpenMode(t *testing.T) {
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("id1", "svc", "svc")},
		logsBody:   tsLine(fakeTS1, `{"level":"error","msg":"boom"}`),
	}
	t.Setenv("DOZOR_API_TOKEN", "")
	t.Setenv("DOZOR_API_ALLOW_INSECURE", "true")
	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=svc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allow-insecure override: want 200, got %d", resp.StatusCode)
	}
}
