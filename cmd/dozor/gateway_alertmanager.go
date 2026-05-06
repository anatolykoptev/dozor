package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// alertmanagerBodyLimit caps the Alertmanager webhook body at 16 KiB.
// Alertmanager payloads are tiny in practice; this guards against mis-routed
// traffic or a loop flooding dozor.
const alertmanagerBodyLimit = 16 * 1024

// alertmanagerPayload is the Prometheus Alertmanager v4 webhook shape.
// Unknown top-level fields are ignored via json.Unmarshal semantics.
type alertmanagerPayload struct {
	Version           string                  `json:"version"`
	GroupKey          string                  `json:"groupKey"`
	Status            string                  `json:"status"`
	Receiver          string                  `json:"receiver"`
	GroupLabels       map[string]string       `json:"groupLabels"`
	CommonLabels      map[string]string       `json:"commonLabels"`
	CommonAnnotations map[string]string       `json:"commonAnnotations"`
	Alerts            []alertmanagerAlertItem `json:"alerts"`
}

// alertmanagerAlertItem is a single alert inside the Alertmanager payload.
type alertmanagerAlertItem struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

// registerAlertmanagerWebhookHandler mounts POST /webhook/alertmanager.
//
// Signature enforcement is opt-in because Alertmanager's default config does
// not send HMAC signatures. Set DOZOR_ALERTMANAGER_REQUIRE_SIG=true when you
// configure Alertmanager with a matching secret.
//
// The notifyAlertFn callback matches the signature in gateway.go so both the
// alert card renderer (satori sidecar) and its plain-text fallback are reused
// transparently.
func registerAlertmanagerWebhookHandler(mx *http.ServeMux, notifyAlertFn func([]engine.Alert, string)) {
	secret := os.Getenv("DOZOR_WEBHOOK_SECRET")
	requireSig := os.Getenv("DOZOR_ALERTMANAGER_REQUIRE_SIG") == "true"

	if !requireSig {
		slog.Warn("alertmanager webhook running without signature enforcement; set DOZOR_ALERTMANAGER_REQUIRE_SIG=true to enforce")
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		key := webhookSourceKey(r)
		if !webhookLimiter.Allow(key) {
			slog.Warn("alertmanager webhook rate-limited", slog.String("source", key))
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, alertmanagerBodyLimit+1))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// LimitReader silently truncates. Detect overflow by comparing read size.
		if len(body) > alertmanagerBodyLimit {
			slog.Warn("alertmanager webhook body exceeds limit",
				slog.Int("limit", alertmanagerBodyLimit),
				slog.Int("received", len(body)))
			http.Error(w, "request body too large", http.StatusBadRequest)
			return
		}

		// Signature check — optional unless DOZOR_ALERTMANAGER_REQUIRE_SIG=true.
		if requireSig {
			if !checkWebhookSignature(body, r, secret) {
				slog.Warn("alertmanager webhook signature mismatch or absent",
					slog.String("source", key))
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		var p alertmanagerPayload
		if err := json.Unmarshal(body, &p); err != nil {
			slog.Warn("alertmanager webhook bad JSON", slog.Any("error", err))
			http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
			return
		}

		if p.Version != "4" {
			slog.Warn("alertmanager webhook unsupported version",
				slog.String("version", p.Version))
			http.Error(w, fmt.Sprintf("unsupported alertmanager version %q (want \"4\")", p.Version), http.StatusBadRequest)
			return
		}

		if len(p.Alerts) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		alerts := make([]engine.Alert, 0, len(p.Alerts))
		for _, item := range p.Alerts {
			alerts = append(alerts, convertAlertmanagerItem(item, p.CommonLabels, p.CommonAnnotations))
		}

		// Build a plain-text summary used as the fallback when card rendering fails.
		fallback := buildAlertmanagerFallback(alerts)
		notifyAlertFn(alerts, fallback)

		slog.Info("alertmanager webhook dispatched",
			slog.Int("alerts", len(alerts)),
			slog.String("status", p.Status))
		w.WriteHeader(http.StatusNoContent)
	}

	mx.HandleFunc("POST /webhook/alertmanager", handler)
}

// convertAlertmanagerItem maps one Alertmanager alert to engine.Alert.
//
// Severity mapping:
//   - "critical" / "error"  → AlertCritical
//   - "warning"             → AlertWarning
//   - absent / unknown      → AlertWarning (default)
//
// Resolved alerts are downgraded to AlertInfo and prefixed with "[RESOLVED] ".
//
// Common* are merged in: when Alertmanager groups alerts and a label/annotation
// is identical across the group, it may be promoted to commonLabels /
// commonAnnotations and absent from the per-alert maps. Per-alert values win.
func convertAlertmanagerItem(item alertmanagerAlertItem, commonLabels, commonAnnotations map[string]string) engine.Alert {
	labels := mergeMap(commonLabels, item.Labels)
	annotations := mergeMap(commonAnnotations, item.Annotations)

	severity := labels["severity"]
	var level engine.AlertLevel
	switch strings.ToLower(severity) {
	case "critical", "error":
		level = engine.AlertCritical
	case "warning":
		level = engine.AlertWarning
	default:
		level = engine.AlertWarning
	}

	service := labels["alertname"]
	if service == "" {
		service = "alertmanager"
	}

	title := annotations["summary"]
	if title == "" {
		status := item.Status
		if status == "" {
			status = "firing"
		}
		// Free-form fallback: NOT a triage-machine-readable line.
		// engine.TriageMachineSep is the separator that ExtractIssues parses;
		// this fallback uses the same em-dash deliberately so visual output is
		// consistent, but the [LEVEL] prefix is missing — ExtractIssues won't
		// (and shouldn't) match it.
		title = service + engine.TriageMachineSep + status
	}

	description := truncateAlertDescription(annotations["description"], 512)

	if strings.EqualFold(item.Status, "resolved") {
		level = engine.AlertInfo
		title = "[RESOLVED] " + title
	}

	return engine.Alert{
		Level:       level,
		Service:     service,
		Title:       title,
		Description: description,
		Timestamp:   item.StartsAt,
	}
}

// mergeMap returns a new map with base entries overridden by override entries.
// Either input may be nil.
func mergeMap(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// truncateAlertDescription truncates s to at most maxBytes bytes, respecting
// UTF-8 rune boundaries so the result is always valid UTF-8.
func truncateAlertDescription(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk backwards from maxBytes to find a valid rune boundary.
	for i := maxBytes; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i] + "…"
		}
	}
	return ""
}

// buildAlertmanagerFallback produces the plain-text string passed as the
// fallback argument to notifyAlertFn when card rendering is unavailable.
func buildAlertmanagerFallback(alerts []engine.Alert) string {
	if len(alerts) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, a := range alerts {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "[%s] %s\n%s", strings.ToUpper(string(a.Level)), a.Title, a.Description)
	}
	return sb.String()
}
