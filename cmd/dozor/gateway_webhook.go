package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	kitratelimit "github.com/anatolykoptev/go-kit/ratelimit"
)

// webhookPayload is the union of fields dozor accepts from external monitors
// (channel-canary, vpn-watchdog, active-probe-canary during decommissioning,
// and arbitrary future sources). Unknown fields are ignored by json.Unmarshal.
type webhookPayload struct {
	Event       string `json:"event"`
	Channel     string `json:"channel"`
	Description string `json:"description"`
	Fails       int    `json:"fails"`
	LastError   string `json:"last_error"`
	Text        string `json:"text"`
	Message     string `json:"message"`
}

// webhookLimiter caps inbound webhook rate per source host. The dangerous
// path is the legacy (no-`event`) branch which posts to the LLM-bearing
// agent loop — a misbehaving sender there could rack up Telegram noise and
// LLM cost. 10 RPS sustained, burst 30 = comfortably above any healthy
// monitor (xray-update fires weekly, awg-watchdog every 30s, Reality
// rotates twice a week) but caps a stuck-in-loop sender.
var webhookLimiter = kitratelimit.NewKeyLimiter(10, 30)

func init() {
	webhookLimiter.StartCleanup(10*time.Minute, 30*time.Minute)
	trustProxy = os.Getenv("DOZOR_TRUST_PROXY") == "1"
}

// webhookSourceKey derives the rate-limit key from the request. Defaults
// to net.SplitHostPort(RemoteAddr); X-Forwarded-For is consulted ONLY when
// DOZOR_TRUST_PROXY=1, because dozor's HTTP server binds *:8765 (publicly
// listenable) and trusting XFF without a real ingress lets any client pin
// itself to a fresh bucket per request, defeating the rate limit entirely.
//
// Set DOZOR_TRUST_PROXY=1 only when dozor is fronted by a proxy that
// strips and re-injects XFF (e.g. Caddy, nginx with `proxy_set_header
// X-Forwarded-For $proxy_add_x_forwarded_for`).
func webhookSourceKey(r *http.Request) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if comma := strings.IndexByte(xff, ','); comma > 0 {
				return strings.TrimSpace(xff[:comma])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return "unknown"
	}
	return host
}

// trustProxy is set at package init from DOZOR_TRUST_PROXY. Default false
// so the publicly-bound default deployment is safe out of the box.
var trustProxy = false

// registerWebhookHandler adds POST /webhook and POST /webhook/ to the mux.
//
// Events are classified by the `event` JSON field so we don't burn an LLM
// loop on noise:
//
//   - channel_dead, channel_recovered → forwarded to the admin TG chat
//     directly via notifyFn (bypasses the agent loop)
//   - active_probe → dropped with 202 (deprecated noise source; pre-empt
//     anything that still POSTs here while we decommission active-probe-canary)
//   - (no `event` field) → legacy path, PublishInbound to the agent loop
//     (preserves vpn-watchdog behaviour, which posts {"message":...})
//   - any other event name → logged + 202, not sent to LLM (deny-by-default
//     so a typo or new source can't re-flood the agent)
func registerWebhookHandler(mx *http.ServeMux, msgBus *bus.Bus, notifyFn func(string)) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		key := webhookSourceKey(r)
		if !webhookLimiter.Allow(key) {
			slog.Warn("webhook rate-limited",
				slog.String("source", key),
				slog.String("path", r.URL.Path))
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		p, legacyText := parseWebhookPayload(body)
		source := r.URL.Path

		switch p.Event {
		case "active_probe":
			slog.Info("webhook dropped (deprecated event)",
				slog.String("event", p.Event),
				slog.String("path", source))
			respondStatus(w, "dropped")

		case "channel_dead", "channel_recovered":
			slog.Info("webhook channel event",
				slog.String("event", p.Event),
				slog.String("channel", p.Channel),
				slog.Int("fails", p.Fails))
			notifyFn(channelEventMessage(p))
			respondStatus(w, "forwarded")

		case "":
			// Legacy path: no `event` field → run through the agent loop.
			// vpn-watchdog.sh posts {"message":"..."} in this shape.
			slog.Info("webhook received (legacy)",
				slog.String("path", source),
				slog.Int("len", len(legacyText)))
			msgBus.PublishInbound(bus.Message{
				ID:        fmt.Sprintf("webhook-%d", time.Now().UnixMilli()),
				Channel:   "internal",
				SenderID:  "webhook",
				ChatID:    "webhook",
				Text:      "ALERT from external monitor (" + source + "):\n\n" + legacyText,
				Timestamp: time.Now(),
			})
			respondStatus(w, "accepted")

		default:
			// Unknown event: log and 202. Do NOT invoke the LLM — keeps a
			// misbehaving source from flooding the agent.
			slog.Warn("webhook unknown event",
				slog.String("event", p.Event),
				slog.String("path", source))
			respondStatus(w, "ignored")
		}
	}

	mx.HandleFunc("POST /webhook", handler)
	mx.HandleFunc("POST /webhook/", handler)
}

// parseWebhookPayload extracts the classified payload and the best available
// textual description (for the legacy no-event path). Returns the raw body
// as text when JSON decoding fails so operators can still see arbitrary
// posts in the agent loop.
func parseWebhookPayload(body []byte) (webhookPayload, string) {
	var p webhookPayload
	text := string(body)
	if err := json.Unmarshal(body, &p); err == nil {
		if p.Text != "" {
			text = p.Text
		} else if p.Message != "" {
			text = p.Message
		}
	}
	return p, text
}

// channelEventMessage picks the sender-supplied message if present, else
// synthesises a one-liner from the structured fields.
func channelEventMessage(p webhookPayload) string {
	if p.Message != "" {
		return p.Message
	}
	return fmt.Sprintf("[channel event] %s %s (fails=%d) %s",
		p.Event, p.Channel, p.Fails, p.LastError)
}

func respondStatus(w http.ResponseWriter, status string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":%q}`, status)
}
