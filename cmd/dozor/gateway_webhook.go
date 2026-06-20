package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/anatolykoptev/dozor/internal/engine"
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

// (HMAC startup warn is logged inline once per registerWebhookHandler call —
// no package-level sync.Once because that breaks test isolation.)

func init() {
	webhookLimiter.StartCleanup(10*time.Minute, 30*time.Minute)
	trustProxy = os.Getenv("DOZOR_TRUST_PROXY") == "1"
}

// checkWebhookSignature verifies X-Dozor-Webhook-Signature (plain hex-encoded
// HMAC-SHA256 of the raw body) against secret. Returns true when the signature
// is valid, or when secret is empty (HMAC disabled).
//
// Header format: X-Dozor-Webhook-Signature: <hex(HMAC-SHA256(body, secret))>
// (no prefix — differs from GitHub's "sha256=" envelope which is used by
// internal/deploy/webhook.go:verifySignature).
func checkWebhookSignature(body []byte, r *http.Request, secret string) bool {
	if secret == "" {
		return true
	}
	sig := r.Header.Get("X-Dozor-Webhook-Signature")
	if sig == "" {
		return false
	}
	sigBytes, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sigBytes, mac.Sum(nil))
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
//   - channel_dead, channel_recovered → rendered as a deterministic alert
//     card via notifyAlertFn (bypasses the agent loop)
//   - active_probe → dropped with 202 (deprecated noise source; pre-empt
//     anything that still POSTs here while we decommission active-probe-canary)
//   - (no `event` field) → legacy path, PublishInbound to the agent loop
//     (preserves vpn-watchdog behaviour, which posts {"message":...})
//   - any other event name → logged + 202, not sent to LLM (deny-by-default
//     so a typo or new source can't re-flood the agent)
//
// A SEPARATE explicit route, POST /webhook/monitor/healthcheck, is registered
// alongside (see monitorHealthcheckHandler). Without it, that path would match
// the POST /webhook/ trailing-slash prefix and fall into the legacy LLM branch
// — exactly what the partner-edge fleet + piter scripts hit. The explicit route
// renders those {"message":...} posts as deterministic alert cards instead.
func registerWebhookHandler(mx *http.ServeMux, msgBus *bus.Bus, notifyFn func(string), notifyAlertFn func([]engine.Alert, string)) {
	secret := os.Getenv("DOZOR_WEBHOOK_SECRET")
	if secret == "" {
		slog.Warn("webhook handler running without HMAC; set DOZOR_WEBHOOK_SECRET to enforce")
	}

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

		if !checkWebhookSignature(body, r, secret) {
			slog.Warn("webhook signature mismatch",
				slog.String("source", key),
				slog.String("path", r.URL.Path))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
			alert := channelEventAlert(p)
			notifyAlertFn([]engine.Alert{alert}, channelEventMessage(p))
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
	mx.HandleFunc("POST /webhook/monitor/healthcheck", monitorHealthcheckHandler(secret, notifyAlertFn))
}

// monitorHealthcheckHandler renders the {"message":"..."} posts from the
// partner-edge fleet (telegram-alert-lib.sh) and the piter/krolik scripts
// (vpn-watchdog, rotate-sni, xray-update, oxpulse-sfu-update) as deterministic
// alert cards.
//
// These senders POST a single pre-formatted prose string and NO structured
// fields, so severity is classified from the text via classifyMonitorMessage —
// purely lexical, NO LLM. The handler NEVER calls msgBus.PublishInbound, so a
// monitor post can never reach the agent loop (the bug this route fixes: Go's
// ServeMux previously matched this path via the POST /webhook/ prefix and fell
// into the legacy LLM branch).
//
// The route is backward-compatible: it parses exactly the payload the existing
// senders already POST, so no edge-fleet redeploy is required.
func monitorHealthcheckHandler(secret string, notifyAlertFn func([]engine.Alert, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := webhookSourceKey(r)
		if !webhookLimiter.Allow(key) {
			slog.Warn("monitor webhook rate-limited",
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
		if !checkWebhookSignature(body, r, secret) {
			slog.Warn("monitor webhook signature mismatch",
				slog.String("source", key),
				slog.String("path", r.URL.Path))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		_, text := parseWebhookPayload(body)
		alert := classifyMonitorMessage(text)

		slog.Info("monitor webhook card",
			slog.String("source", key),
			slog.String("level", string(alert.Level)),
			slog.Int("len", len(text)))
		notifyAlertFn([]engine.Alert{alert}, text)
		respondStatus(w, "carded")
	}
}

// monitor severity keyword sets, checked in priority order (critical wins over
// error wins over recovered). All matching is case-insensitive substring on the
// lower-cased message. An unmatched message defaults to AlertWarning — these
// scripts mostly post routine update/rotation/failover events, and warning is
// the safe non-alarming-but-visible default that still renders a card.
var (
	monitorCriticalWords  = []string{"dead", "down", "fail", "unreachable", "outage", "critical", "blackhole", "blocked"}
	monitorErrorWords     = []string{"error", "degraded", "stale", "timeout", "stall", "drop"}
	monitorRecoveredWords = []string{"recovered", "restored", "back up", "healthy", "success", "ok", " up ", "switched"}
)

// classifyMonitorMessage maps a free-form monitor message to an engine.Alert
// with a deterministically-chosen severity. Pure lexical classification — no LLM.
func classifyMonitorMessage(message string) engine.Alert {
	text := strings.TrimSpace(message)
	if text == "" {
		text = "(empty monitor message)"
	}
	lower := strings.ToLower(text)

	// Transition messages have the shape "... <prev> -> <cur>" (partner-edge
	// channel-health, vpn-watchdog). The TARGET state (after the last arrow)
	// is what determines severity — "dead -> recovered" is a recovery, not an
	// outage. Classify on the post-arrow slice so the prev state can't bias it.
	classifyOn := lower
	if i := strings.LastIndex(lower, "->"); i >= 0 {
		classifyOn = lower[i+2:]
	}

	level := engine.AlertWarning // safe default for routine update/rotation events
	switch {
	case containsAny(classifyOn, monitorCriticalWords):
		level = engine.AlertCritical
	case containsAny(classifyOn, monitorErrorWords):
		level = engine.AlertError
	case containsAny(classifyOn, monitorRecoveredWords):
		level = engine.AlertInfo
	}

	// The message is the human-facing line; use its first line as the title and
	// keep the full text as the description so multi-line prose survives.
	title := text
	if nl := strings.IndexByte(title, '\n'); nl >= 0 {
		title = title[:nl]
	}
	description := ""
	if title != text {
		description = text
	}

	return engine.Alert{
		Level:       level,
		Service:     "monitor",
		Title:       title,
		Description: description,
		Timestamp:   time.Now(),
	}
}

// containsAny reports whether haystack contains any of needles.
func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// channelEventAlert maps a classified channel_dead / channel_recovered webhook
// to an engine.Alert so it renders as a card like every other service-ops alert.
// channel_recovered is informational; channel_dead is critical.
func channelEventAlert(p webhookPayload) engine.Alert {
	level := engine.AlertCritical
	if p.Event == "channel_recovered" {
		level = engine.AlertInfo
	}
	title := channelEventMessage(p)
	desc := ""
	if p.LastError != "" {
		desc = "last_error: " + p.LastError
	}
	service := "channel"
	if p.Channel != "" {
		service = "channel:" + p.Channel
	}
	return engine.Alert{
		Level:       level,
		Service:     service,
		Title:       title,
		Description: desc,
		Timestamp:   time.Now(),
	}
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
