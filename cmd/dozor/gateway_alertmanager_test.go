package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// makeAlertmanagerHandler builds an isolated mux with the alertmanager webhook
// registered, using the supplied notifyAlertFn spy and optional secret / require-sig setting.
func makeAlertmanagerHandler(
	t *testing.T,
	webhookSecret string,
	requireSig string, // "true" / "false" / ""
	spy func([]engine.Alert, string),
) http.Handler {
	t.Helper()
	t.Setenv("DOZOR_WEBHOOK_SECRET", webhookSecret)
	t.Setenv("DOZOR_ALERTMANAGER_REQUIRE_SIG", requireSig)
	mx := http.NewServeMux()
	registerAlertmanagerWebhookHandler(mx, spy)
	return mx
}

// captureAlerts returns a spy notifyAlertFn that accumulates dispatched alerts.
func captureAlerts(t *testing.T) (spy func([]engine.Alert, string), received func() []engine.Alert) {
	t.Helper()
	var mu sync.Mutex
	var all []engine.Alert
	spy = func(alerts []engine.Alert, _ string) {
		mu.Lock()
		defer mu.Unlock()
		all = append(all, alerts...)
	}
	received = func() []engine.Alert {
		mu.Lock()
		defer mu.Unlock()
		out := make([]engine.Alert, len(all))
		copy(out, all)
		return out
	}
	return spy, received
}

// TestAlertmanager_FiringCritical — one firing alert with critical severity → 204,
// dispatched with engine.AlertCritical level.
func TestAlertmanager_FiringCritical(t *testing.T) {
	spy, received := captureAlerts(t)
	handler := makeAlertmanagerHandler(t, "", "", spy)

	body := `{
		"version":"4",
		"groupKey":"{}:{}",
		"status":"firing",
		"receiver":"dozor",
		"groupLabels":{},
		"commonLabels":{"alertname":"InstanceDown","severity":"critical"},
		"alerts":[{
			"status":"firing",
			"labels":{"alertname":"InstanceDown","instance":"server:9090","severity":"critical"},
			"annotations":{"summary":"Server down","description":"Instance has been down for 5 minutes"},
			"startsAt":"2026-05-06T10:00:00Z",
			"endsAt":"0001-01-01T00:00:00Z"
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rr.Code, rr.Body.String())
	}

	// Allow tiny goroutine window (none needed here — dispatch is synchronous, but defensive).

	alerts := received()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 dispatched alert, got %d", len(alerts))
	}
	a := alerts[0]
	if a.Level != engine.AlertCritical {
		t.Errorf("level: got %q, want %q", a.Level, engine.AlertCritical)
	}
	if a.Service != "InstanceDown" {
		t.Errorf("service: got %q, want %q", a.Service, "InstanceDown")
	}
	if a.Title != "Server down" {
		t.Errorf("title: got %q, want %q", a.Title, "Server down")
	}
	if !strings.Contains(a.Description, "down for 5 minutes") {
		t.Errorf("description: got %q, want it to contain 'down for 5 minutes'", a.Description)
	}
}

// TestAlertmanager_TwoAlerts — firing + resolved in same payload → 204, both dispatched;
// resolved alert gets [RESOLVED] prefix and AlertInfo level.
func TestAlertmanager_TwoAlerts(t *testing.T) {
	spy, received := captureAlerts(t)
	handler := makeAlertmanagerHandler(t, "", "", spy)

	body := `{
		"version":"4",
		"status":"firing",
		"receiver":"dozor",
		"groupLabels":{},
		"commonLabels":{},
		"alerts":[
			{
				"status":"firing",
				"labels":{"alertname":"DiskFull","severity":"warning"},
				"annotations":{"summary":"Disk almost full","description":"Disk at 90%"},
				"startsAt":"2026-05-06T10:00:00Z",
				"endsAt":"0001-01-01T00:00:00Z"
			},
			{
				"status":"resolved",
				"labels":{"alertname":"MemoryHigh","severity":"critical"},
				"annotations":{"summary":"Memory OK","description":"Memory back to normal"},
				"startsAt":"2026-05-06T09:00:00Z",
				"endsAt":"2026-05-06T10:00:00Z"
			}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rr.Code, rr.Body.String())
	}


	alerts := received()
	if len(alerts) != 2 {
		t.Fatalf("expected 2 dispatched alerts, got %d", len(alerts))
	}

	// First is firing/warning.
	if alerts[0].Level != engine.AlertWarning {
		t.Errorf("alert[0] level: got %q, want %q", alerts[0].Level, engine.AlertWarning)
	}

	// Second is resolved: downgraded to AlertInfo, title prefixed.
	if alerts[1].Level != engine.AlertInfo {
		t.Errorf("alert[1] level: got %q, want %q", alerts[1].Level, engine.AlertInfo)
	}
	if !strings.HasPrefix(alerts[1].Title, "[RESOLVED]") {
		t.Errorf("alert[1] title: got %q, want [RESOLVED] prefix", alerts[1].Title)
	}
}

// TestAlertmanager_EmptyAlerts — empty alerts array → 204, nothing dispatched.
func TestAlertmanager_EmptyAlerts(t *testing.T) {
	var called atomic.Int32
	spy := func(alerts []engine.Alert, _ string) { called.Add(int32(len(alerts))) }
	handler := makeAlertmanagerHandler(t, "", "", spy)

	body := `{"version":"4","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},"alerts":[]}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rr.Code, rr.Body.String())
	}
	if n := called.Load(); n != 0 {
		t.Errorf("expected no dispatched alerts, got %d", n)
	}
}

// TestAlertmanager_VersionGate — version "5" → 400.
func TestAlertmanager_VersionGate(t *testing.T) {
	spy := func([]engine.Alert, string) {}
	handler := makeAlertmanagerHandler(t, "", "", spy)

	body := `{"version":"5","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},"alerts":[]}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for version 5, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestAlertmanager_BodyTooLarge — body > 16 KiB → 400.
func TestAlertmanager_BodyTooLarge(t *testing.T) {
	spy := func([]engine.Alert, string) {}
	handler := makeAlertmanagerHandler(t, "", "", spy)

	// Build a body > 16384 bytes by stuffing a huge description field.
	prefix := `{"version":"4","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},"alerts":[{"status":"firing","labels":{"alertname":"Flood"},"annotations":{"description":"`
	suffix := `"},"startsAt":"2026-05-06T10:00:00Z","endsAt":"0001-01-01T00:00:00Z"}]}`
	padding := strings.Repeat("x", 17000)
	body := prefix + padding + suffix

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized body, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestAlertmanager_HMAC_RequireSig_ValidSig — DOZOR_ALERTMANAGER_REQUIRE_SIG=true + valid HMAC → 204.
func TestAlertmanager_HMAC_RequireSig_ValidSig(t *testing.T) {
	const secret = "alertmanager-secret"
	spy, received := captureAlerts(t)
	handler := makeAlertmanagerHandler(t, secret, "true", spy)

	body := `{"version":"4","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},"alerts":[{"status":"firing","labels":{"alertname":"Test"},"annotations":{},"startsAt":"2026-05-06T10:00:00Z","endsAt":"0001-01-01T00:00:00Z"}]}`
	sig := signBody(secret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dozor-Webhook-Signature", sig)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 with valid sig, got %d body=%s", rr.Code, rr.Body.String())
	}
	if n := len(received()); n != 1 {
		t.Errorf("expected 1 dispatched alert, got %d", n)
	}
}

// TestAlertmanager_HMAC_RequireSig_MissingSig — DOZOR_ALERTMANAGER_REQUIRE_SIG=true + no header → 401.
func TestAlertmanager_HMAC_RequireSig_MissingSig(t *testing.T) {
	const secret = "alertmanager-secret"
	spy := func([]engine.Alert, string) {}
	handler := makeAlertmanagerHandler(t, secret, "true", spy)

	body := `{"version":"4","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},"alerts":[]}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Dozor-Webhook-Signature header.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with missing sig, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestAlertmanager_UnknownSeverity — absent / unknown severity label → AlertWarning.
func TestAlertmanager_UnknownSeverity(t *testing.T) {
	spy, received := captureAlerts(t)
	handler := makeAlertmanagerHandler(t, "", "", spy)

	body := `{
		"version":"4",
		"status":"firing",
		"receiver":"dozor",
		"groupLabels":{},
		"commonLabels":{},
		"alerts":[{
			"status":"firing",
			"labels":{"alertname":"WeirdAlert","severity":"ultramegacrash"},
			"annotations":{"summary":"Something odd"},
			"startsAt":"2026-05-06T10:00:00Z",
			"endsAt":"0001-01-01T00:00:00Z"
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rr.Code, rr.Body.String())
	}
	alerts := received()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != engine.AlertWarning {
		t.Errorf("unknown severity: got %q, want %q", alerts[0].Level, engine.AlertWarning)
	}
}

// TestAlertmanager_NoSigRequireDefault — DOZOR_ALERTMANAGER_REQUIRE_SIG unset (default false),
// secret set but no sig header → still accepted (alertmanager doesn't sign by default).
func TestAlertmanager_NoSigRequireDefault(t *testing.T) {
	const secret = "alertmanager-secret"
	spy, received := captureAlerts(t)
	// requireSig="" → default false
	handler := makeAlertmanagerHandler(t, secret, "", spy)

	body := `{"version":"4","status":"firing","receiver":"dozor","groupLabels":{},"commonLabels":{},"alerts":[{"status":"firing","labels":{"alertname":"Test"},"annotations":{},"startsAt":"2026-05-06T10:00:00Z","endsAt":"0001-01-01T00:00:00Z"}]}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No sig header — should pass when REQUIRE_SIG is false
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 when require_sig=false, got %d body=%s", rr.Code, rr.Body.String())
	}
	if n := len(received()); n != 1 {
		t.Errorf("expected 1 dispatched alert, got %d", n)
	}
}

// TestAlertmanager_BadJSON — malformed JSON → 400.
func TestAlertmanager_BadJSON(t *testing.T) {
	spy := func([]engine.Alert, string) {}
	handler := makeAlertmanagerHandler(t, "", "", spy)

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad JSON, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestAlertmanager_TruncateUTF8 — description with multibyte cut point must
// NOT split a codepoint. 512-byte cap on a Russian/CJK string lands mid-rune;
// truncateAlertDescription walks back to the previous boundary.
func TestAlertmanager_TruncateUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string // expected exact prefix; "…" appended automatically
	}{
		// Cyrillic rune is 2 bytes. Cap=5: bytes 0..3 are 2 runes (Пр), byte 4
		// is start of "и", byte 5 is continuation. Walk back from i=5: s[5]
		// is continuation (skip), s[4]=0xD0 is RuneStart → return s[:4] + "…".
		{"cyrillic mid-rune", "Привет, мир!", 5, "Пр…"},
		// CJK rune is 3 bytes. Cap=4: byte 3 is start of 2nd char, byte 4
		// is continuation. Walk back: s[4] continuation, s[3]=RuneStart → s[:3] + "…".
		{"chinese mid-rune", "配额超出限制", 4, "配…"},
		// 4-byte emoji. Cap=2 < 4: no rune fits. Loop walks back without
		// finding RuneStart in s[1..2] (both continuation bytes) → returns "".
		// Documents current behavior — caller loses signal when cap < first rune.
		{"emoji mid-rune cap below rune size", "🔥abc", 2, ""},
		// ASCII-only at exact boundary returns full string (no truncation).
		{"ascii under cap", "hello", 100, "hello"},
		// ASCII over cap, boundary lands cleanly.
		{"ascii over cap", "abcdefghij", 5, "abcde…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateAlertDescription(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateAlertDescription(%q,%d)=%q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// TestAlertmanager_CommonLabelsAndAnnotations — when per-alert labels/annotations
// are empty but commonLabels/commonAnnotations carry the data (Alertmanager
// promotes shared values), the converter must merge so service+title aren't lost.
func TestAlertmanager_CommonLabelsAndAnnotations(t *testing.T) {
	spy, received := captureAlerts(t)
	handler := makeAlertmanagerHandler(t, "", "", spy)

	// Per-alert labels/annotations are empty; commonLabels/commonAnnotations
	// carry the alertname + summary. Converter must use them.
	body := `{"version":"4","status":"firing","receiver":"dozor",` +
		`"commonLabels":{"alertname":"DiskFull","severity":"critical"},` +
		`"commonAnnotations":{"summary":"Disk pressure on /mnt/cargo","description":"82% used"},` +
		`"alerts":[{"status":"firing","labels":{},"annotations":{},"startsAt":"2026-05-06T10:00:00Z","endsAt":"0001-01-01T00:00:00Z"}]}`

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rr.Code, rr.Body.String())
	}
	got := received()
	if len(got) != 1 {
		t.Fatalf("expected 1 alert dispatched, got %d", len(got))
	}
	a := got[0]
	if a.Service != "DiskFull" {
		t.Errorf("Service: want DiskFull (from commonLabels), got %q", a.Service)
	}
	if a.Level != engine.AlertCritical {
		t.Errorf("Level: want AlertCritical (from commonLabels.severity), got %q", a.Level)
	}
	if !strings.Contains(a.Title, "Disk pressure") {
		t.Errorf("Title: want commonAnnotations.summary, got %q", a.Title)
	}
	if !strings.Contains(a.Description, "82%") {
		t.Errorf("Description: want commonAnnotations.description, got %q", a.Description)
	}
}

// TestAlertmanager_BatchOf3_SingleNotifyCall verifies that a webhook POST
// carrying 3 alerts triggers exactly one notifyAlertFn invocation with all
// 3 alerts in the slice — NOT three separate calls. This is the regression
// guard for the "concurrent render cascade" bug where N alerts → N satori
// requests → N-1 timeout failures.
func TestAlertmanager_BatchOf3_SingleNotifyCall(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    int
		allAlerts []engine.Alert
	)
	spy := func(alerts []engine.Alert, _ string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		allAlerts = append(allAlerts, alerts...)
	}
	handler := makeAlertmanagerHandler(t, "", "", spy)

	body := `{
		"version":"4",
		"status":"firing",
		"receiver":"dozor",
		"groupLabels":{},
		"commonLabels":{},
		"alerts":[
			{
				"status":"firing",
				"labels":{"alertname":"DozorBuildFailingPersistent","severity":"critical"},
				"annotations":{"summary":"Build failing","description":"Build has been failing for 20m"},
				"startsAt":"2026-05-15T19:15:17Z",
				"endsAt":"0001-01-01T00:00:00Z"
			},
			{
				"status":"firing",
				"labels":{"alertname":"RealitySniPoolDrift","severity":"warning"},
				"annotations":{"summary":"SNI pool drifted","description":"Pool size diverged from expected"},
				"startsAt":"2026-05-15T19:15:17Z",
				"endsAt":"0001-01-01T00:00:00Z"
			},
			{
				"status":"firing",
				"labels":{"alertname":"PartnerEdgeStaleHeartbeat","severity":"warning"},
				"annotations":{"summary":"Stale heartbeat","description":"Last heartbeat >5m ago"},
				"startsAt":"2026-05-15T19:15:17Z",
				"endsAt":"0001-01-01T00:00:00Z"
			}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rr.Code, rr.Body.String())
	}

	mu.Lock()
	gotCalls := calls
	gotAlerts := len(allAlerts)
	mu.Unlock()

	// Critical invariant: single POST → single notifyAlertFn call (not N calls).
	if gotCalls != 1 {
		t.Errorf("notifyAlertFn called %d times, want exactly 1 (batch regression)", gotCalls)
	}
	if gotAlerts != 3 {
		t.Errorf("total alerts dispatched: got %d, want 3", gotAlerts)
	}
}

// Compile-time interface check: registerAlertmanagerWebhookHandler must exist.
var _ = registerAlertmanagerWebhookHandler
