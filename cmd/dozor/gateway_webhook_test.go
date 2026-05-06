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
		registerWebhookHandler(mx, msgBus, func(string) {})
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

// TestWebhookHandler_EventClassifier covers the routing logic introduced to
// silence ACTIVE_PROBE webhook spam and route channel_dead/recovered events
// straight to Telegram without invoking the LLM loop.
func TestWebhookHandler_EventClassifier(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantInbound    bool
		wantNotify     bool
		wantStatusBody string
	}{
		{
			name:           "legacy_no_event_field_publishes_to_bus",
			body:           `{"message":"[piter] VPN WATCHDOG: Switched to ch2"}`,
			wantInbound:    true,
			wantNotify:     false,
			wantStatusBody: `"accepted"`,
		},
		{
			name:           "active_probe_dropped_silently",
			body:           `{"event":"active_probe","src_ip":"70.34.243.184","container":"xray-reality"}`,
			wantInbound:    false,
			wantNotify:     false,
			wantStatusBody: `"dropped"`,
		},
		{
			name:           "channel_dead_forwards_to_telegram_no_llm",
			body:           `{"event":"channel_dead","channel":"ch1","fails":3,"message":"[piter] CH1 DEAD"}`,
			wantInbound:    false,
			wantNotify:     true,
			wantStatusBody: `"forwarded"`,
		},
		{
			name:           "channel_recovered_forwards_to_telegram",
			body:           `{"event":"channel_recovered","channel":"ch1","fails":0,"message":"[piter] CH1 RECOVERED"}`,
			wantInbound:    false,
			wantNotify:     true,
			wantStatusBody: `"forwarded"`,
		},
		{
			name:           "unknown_event_ignored",
			body:           `{"event":"some_future_thing","data":"whatever"}`,
			wantInbound:    false,
			wantNotify:     false,
			wantStatusBody: `"ignored"`,
		},
		{
			name:           "channel_dead_without_message_synthesizes_one",
			body:           `{"event":"channel_dead","channel":"ch2","fails":3,"last_error":"stale handshake"}`,
			wantInbound:    false,
			wantNotify:     true,
			wantStatusBody: `"forwarded"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.New()
			defer msgBus.Close()

			var notifyCount atomic.Int32
			var notifyMsg atomic.Value
			notifyFn := func(text string) {
				notifyCount.Add(1)
				notifyMsg.Store(text)
			}

			mx := http.NewServeMux()
			registerWebhookHandler(mx, msgBus, notifyFn)

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

			req := httptest.NewRequest(http.MethodPost, "/webhook/monitor/healthcheck",
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
		})
	}
}
