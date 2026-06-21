package tools

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// resetDefaultRing replaces the package-level ring with a fresh one for test isolation.
// Caller must restore via defer.
func resetDefaultRing(capacity int) func() {
	old := engine.DefaultAlertRing
	engine.DefaultAlertRing = engine.NewAlertRing(capacity)
	return func() { engine.DefaultAlertRing = old }
}

func TestHandleAlertsActive_Defaults(t *testing.T) {
	// since="" → 1h, limit=0 → 50, firing included by default.
	// Point Alertmanager at an unreachable addr so we get a warning but no error.
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")

	defer resetDefaultRing(10)()

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unreachable Alertmanager → warning, not error.
	if len(out.Warnings) == 0 {
		t.Error("expected at least one warning for unreachable alertmanager")
	}
	// Firing slice must be non-nil (empty, not null).
	if out.Firing == nil {
		t.Error("Firing must be non-nil slice")
	}
	// Recent from empty ring → empty non-nil slice.
	if out.Recent == nil {
		t.Error("Recent must be non-nil slice")
	}
	// Verdict should mention "1h".
	if out.Verdict == "" {
		t.Error("Verdict must not be empty")
	}
}

func TestHandleAlertsActive_DefaultSince1h(t *testing.T) {
	// When Since is empty the verdict label must say "1h".
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")
	defer resetDefaultRing(10)()

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "0 firing, 0 recent (1h)"
	if out.Verdict != want {
		t.Errorf("verdict: want %q, got %q", want, out.Verdict)
	}
}

func TestHandleAlertsActive_DefaultLimitIs50(t *testing.T) {
	// Ring with 60 entries — limit=0 must return 50 (defaultAlertsLimit).
	defer resetDefaultRing(200)()

	now := time.Now()
	for i := range 60 {
		engine.DefaultAlertRing.Add(engine.Alert{
			Level:     engine.AlertWarning,
			Service:   "svc",
			Title:     "t",
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
		})
	}

	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Recent) != alertsDefaultLimit {
		t.Errorf("expected %d recent entries (default limit), got %d", alertsDefaultLimit, len(out.Recent))
	}
}

func TestHandleAlertsActive_ExcludeFiringDefault(t *testing.T) {
	// By default ExcludeFiring=false, so Alertmanager IS called.
	// We detect this by setting a reachable-but-bad URL: warning appears.
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")
	defer resetDefaultRing(5)()

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{ExcludeFiring: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// If Alertmanager was called we expect a warning; if it was skipped we would NOT get a warning.
	if len(out.Warnings) == 0 {
		t.Error("expected warning because Alertmanager was called (ExcludeFiring=false)")
	}
}

func TestHandleAlertsActive_ExcludeFiringTrue(t *testing.T) {
	// ExcludeFiring=true → no Alertmanager call → no warnings (ring is empty).
	// Do NOT set the env var so a misconfigured call would be obvious.
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093") // still set to detect accidental calls
	defer resetDefaultRing(5)()

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{ExcludeFiring: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No Alertmanager call → no warning from that source.
	for _, w := range out.Warnings {
		if len(w) > len("alertmanager: ") && w[:len("alertmanager: ")] == "alertmanager: " {
			t.Errorf("ExcludeFiring=true but got alertmanager warning: %s", w)
		}
	}
	// Firing must be empty non-nil.
	if out.Firing == nil {
		t.Error("Firing must be non-nil even when excluded")
	}
	if len(out.Firing) != 0 {
		t.Errorf("expected 0 firing when excluded, got %d", len(out.Firing))
	}
}

func TestHandleAlertsActive_RingReadback(t *testing.T) {
	// Add alerts to DefaultAlertRing; assert they surface in Recent.
	defer resetDefaultRing(20)()
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")

	now := time.Now()
	engine.DefaultAlertRing.Add(engine.Alert{
		Level:     engine.AlertCritical,
		Service:   "deploy-service",
		Title:     "deploy failed",
		Timestamp: now.Add(-5 * time.Minute),
	})
	engine.DefaultAlertRing.Add(engine.Alert{
		Level:     engine.AlertWarning,
		Service:   "watch-service",
		Title:     "health miss",
		Timestamp: now.Add(-1 * time.Minute),
	})

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{Since: "1h", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Recent) != 2 {
		t.Fatalf("expected 2 ring entries, got %d", len(out.Recent))
	}
	// Newest-first: watch-service should be first.
	if out.Recent[0].Service != "watch-service" {
		t.Errorf("expected watch-service first (newest), got %s", out.Recent[0].Service)
	}
	if out.Recent[1].Service != "deploy-service" {
		t.Errorf("expected deploy-service second, got %s", out.Recent[1].Service)
	}
}

func TestHandleAlertsActive_AlertmanagerUnreachable_NoError(t *testing.T) {
	// Alertmanager unreachable → warning folded in, call still succeeds.
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")
	defer resetDefaultRing(5)()

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{})
	if err != nil {
		t.Fatalf("call must succeed even when Alertmanager is unreachable; got error: %v", err)
	}
	if len(out.Warnings) == 0 {
		t.Error("expected warning for unreachable Alertmanager")
	}
	// Firing slice must not be nil.
	if out.Firing == nil {
		t.Error("Firing must be non-nil after unreachable Alertmanager")
	}
}

func TestHandleAlertsActive_InvalidSince_ReturnsError(t *testing.T) {
	_, err := handleAlertsActive(context.Background(), AlertsActiveInput{Since: "banana"})
	if err == nil {
		t.Error("expected error for invalid since value")
	}
}

func TestHandleAlertsActive_Verdict(t *testing.T) {
	defer resetDefaultRing(20)()
	t.Setenv("DOZOR_ALERTMANAGER_URL", "http://127.0.0.1:19093")

	engine.DefaultAlertRing.Add(engine.Alert{
		Level:     engine.AlertCritical,
		Service:   "x",
		Title:     "t",
		Timestamp: time.Now().Add(-5 * time.Minute),
	})

	out, err := handleAlertsActive(context.Background(), AlertsActiveInput{Since: "30m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Alertmanager unreachable → 0 firing; 1 recent; since label "30m".
	want := "0 firing, 1 recent (30m)"
	if out.Verdict != want {
		t.Errorf("verdict: want %q, got %q", want, out.Verdict)
	}
}
