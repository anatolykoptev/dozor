package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
)

// signBody computes the HMAC-SHA256 hex of body using secret, matching
// the X-Dozor-Webhook-Signature header format.
func signBody(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestWebhookHandler_HMAC covers the optional HMAC-SHA256 signature gate on
// the webhook handler, controlled by DOZOR_WEBHOOK_SECRET.
func TestWebhookHandler_HMAC(t *testing.T) {
	const secret = "test-secret-123"
	const body = `{"event":"channel_dead","channel":"ch1","fails":1,"message":"dead"}`

	makeHandler := func(t *testing.T, envSecret string) (http.Handler, *bus.Bus) {
		t.Helper()
		t.Setenv("DOZOR_WEBHOOK_SECRET", envSecret)
		msgBus := bus.New()
		t.Cleanup(func() { msgBus.Close() })
		mx := http.NewServeMux()
		registerWebhookHandler(mx, msgBus, func(string) {}, func([]engine.Alert, string) {})
		return mx, msgBus
	}

	t.Run("valid_sig_accepted_when_secret_set", func(t *testing.T) {
		handler, _ := makeHandler(t, secret)
		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dozor-Webhook-Signature", signBody(secret, body))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("valid sig: expected 200, got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("bad_sig_rejected_401_when_secret_set", func(t *testing.T) {
		handler, _ := makeHandler(t, secret)
		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dozor-Webhook-Signature", "badc0ffee")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("bad sig: expected 401, got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("missing_sig_rejected_401_when_secret_set", func(t *testing.T) {
		handler, _ := makeHandler(t, secret)
		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// No X-Dozor-Webhook-Signature header.
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("missing sig: expected 401, got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("no_check_when_secret_unset_back_compat", func(t *testing.T) {
		handler, _ := makeHandler(t, "") // empty secret
		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// No signature header at all.
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("no secret: expected 200 (back-compat), got %d body=%s", rr.Code, rr.Body.String())
		}
	})
}

// TestWebhookHandler_EventClassifier covers the routing logic on the
// POST /webhook path: silence active_probe spam, render channel_dead/recovered
// as deterministic alert CARDS (notifyAlertFn, no LLM), and keep the legacy
// no-event path on the agent loop. Channel events now go through notifyAlertFn,
// NOT the plain-text notifyFn, so all service-ops alerts render identically.
func TestWebhookHandler_EventClassifier(t *testing.T) {
	cases := []struct {
		name           string
		path           string
		body           string
		wantInbound    bool
		wantNotify     bool // plain-text notifyFn
		wantAlertCard  bool // notifyAlertFn (card path)
		wantStatusBody string
	}{
		{
			name:           "legacy_no_event_field_publishes_to_bus",
			path:           "/webhook",
			body:           `{"message":"some opaque external alert"}`,
			wantInbound:    true,
			wantStatusBody: `"accepted"`,
		},
		{
			name:           "active_probe_dropped_silently",
			path:           "/webhook",
			body:           `{"event":"active_probe","src_ip":"70.34.243.184","container":"xray-reality"}`,
			wantStatusBody: `"dropped"`,
		},
		{
			name:           "channel_dead_renders_card_no_llm",
			path:           "/webhook",
			body:           `{"event":"channel_dead","channel":"ch1","fails":3,"message":"[piter] CH1 DEAD"}`,
			wantAlertCard:  true,
			wantStatusBody: `"forwarded"`,
		},
		{
			name:           "channel_recovered_renders_card",
			path:           "/webhook",
			body:           `{"event":"channel_recovered","channel":"ch1","fails":0,"message":"[piter] CH1 RECOVERED"}`,
			wantAlertCard:  true,
			wantStatusBody: `"forwarded"`,
		},
		{
			name:           "unknown_event_ignored",
			path:           "/webhook",
			body:           `{"event":"some_future_thing","data":"whatever"}`,
			wantStatusBody: `"ignored"`,
		},
		{
			name:           "channel_dead_without_message_synthesizes_one",
			path:           "/webhook",
			body:           `{"event":"channel_dead","channel":"ch2","fails":3,"last_error":"stale handshake"}`,
			wantAlertCard:  true,
			wantStatusBody: `"forwarded"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.New()
			defer msgBus.Close()

			var notifyCount, alertCount atomic.Int32
			notifyFn := func(string) { notifyCount.Add(1) }
			notifyAlertFn := func([]engine.Alert, string) { alertCount.Add(1) }

			mx := http.NewServeMux()
			registerWebhookHandler(mx, msgBus, notifyFn, notifyAlertFn)

			// Drain inbound bus to observe publishes.
			var inboundCount atomic.Int32
			var wg sync.WaitGroup
			consumeCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
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

			req := httptest.NewRequest(http.MethodPost, tc.path,
				bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			mx.ServeHTTP(rr, req)

			// Give the bus goroutine a moment to drain.
			time.Sleep(20 * time.Millisecond)
			cancel()
			wg.Wait()

			if rr.Code != http.StatusOK {
				t.Fatalf("unexpected status %d, body: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantStatusBody) {
				t.Errorf("body = %q, want substring %q", rr.Body.String(), tc.wantStatusBody)
			}
			if got := inboundCount.Load(); tc.wantInbound && got == 0 {
				t.Errorf("expected bus.PublishInbound, got none")
			} else if !tc.wantInbound && got != 0 {
				t.Errorf("expected NO bus.PublishInbound, got %d", got)
			}
			if got := notifyCount.Load(); tc.wantNotify && got == 0 {
				t.Errorf("expected notifyFn call, got none")
			} else if !tc.wantNotify && got != 0 {
				t.Errorf("expected NO notifyFn call, got %d", got)
			}
			if got := alertCount.Load(); tc.wantAlertCard && got == 0 {
				t.Errorf("expected notifyAlertFn (card) call, got none")
			} else if !tc.wantAlertCard && got != 0 {
				t.Errorf("expected NO notifyAlertFn call, got %d", got)
			}
		})
	}
}

// TestMonitorHealthcheckHandler_RendersCardNeverLLM is the regression guard for
// the consolidation: the partner-edge fleet + piter scripts POST
// {"message":"..."} to /webhook/monitor/healthcheck. That path MUST render a
// deterministic alert card (notifyAlertFn) and MUST NEVER reach the agent loop
// (PublishInbound). Severity is classified lexically from the message text.
func TestMonitorHealthcheckHandler_RendersCardNeverLLM(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantLevel engine.AlertLevel
	}{
		{
			name:      "edge_transition_down_is_critical",
			body:      `{"message":"[edge-ru1] TRANSITION upstream=coturn-tls healthy -> dead"}`,
			wantLevel: engine.AlertCritical,
		},
		{
			name:      "vpn_failover_default_warning",
			body:      `{"message":"[piter] SNI rotated to cloudflare.com"}`,
			wantLevel: engine.AlertWarning,
		},
		{
			name:      "recovered_is_info",
			body:      `{"message":"[edge-ru1] TRANSITION upstream=coturn-udp dead -> recovered"}`,
			wantLevel: engine.AlertInfo,
		},
		{
			name:      "stale_handshake_is_error",
			body:      `{"message":"[motherly] xray-update: stale config detected"}`,
			wantLevel: engine.AlertError,
		},
		{
			name:      "malformed_non_json_safe_default",
			body:      `not json at all`,
			wantLevel: engine.AlertWarning,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.New()
			defer msgBus.Close()

			var gotAlerts atomic.Value
			var alertCount atomic.Int32
			notifyAlertFn := func(alerts []engine.Alert, _ string) {
				alertCount.Add(1)
				gotAlerts.Store(alerts)
			}

			mx := http.NewServeMux()
			registerWebhookHandler(mx, msgBus, func(string) {}, notifyAlertFn)

			// Watch the inbound bus — it must stay empty (no LLM loop).
			var inboundCount atomic.Int32
			var wg sync.WaitGroup
			consumeCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
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

			req := httptest.NewRequest(http.MethodPost, "/webhook/monitor/healthcheck",
				bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			mx.ServeHTTP(rr, req)

			time.Sleep(20 * time.Millisecond)
			cancel()
			wg.Wait()

			if rr.Code != http.StatusOK {
				t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), `"carded"`) {
				t.Errorf("body = %q, want carded", rr.Body.String())
			}
			if got := inboundCount.Load(); got != 0 {
				t.Errorf("monitor message reached the LLM loop (PublishInbound=%d) — must NEVER happen", got)
			}
			if got := alertCount.Load(); got != 1 {
				t.Fatalf("notifyAlertFn called %d times, want exactly 1", got)
			}
			alerts, _ := gotAlerts.Load().([]engine.Alert)
			if len(alerts) != 1 {
				t.Fatalf("got %d alerts, want 1", len(alerts))
			}
			if alerts[0].Level != tc.wantLevel {
				t.Errorf("level = %q, want %q (title=%q)", alerts[0].Level, tc.wantLevel, alerts[0].Title)
			}
		})
	}
}
