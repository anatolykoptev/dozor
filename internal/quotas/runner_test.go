package quotas

import (
	"context"
	"slices"
	"testing"

	"github.com/anatolykoptev/dozor/internal/quotas/probe"
)

// stubProber is a minimal Prober for testing.
type stubProber struct {
	vendor   string
	readings []probe.Reading
	err      error
}

func (s *stubProber) Vendor() string { return s.vendor }
func (s *stubProber) Probe(_ context.Context) ([]probe.Reading, error) {
	return s.readings, s.err
}

// fakeProber records actual Probe invocations via an onCall hook.
type fakeProber struct {
	nameStr  string
	readings []probe.Reading
	onCall   func()
}

func (f *fakeProber) Vendor() string { return f.nameStr }
func (f *fakeProber) Probe(_ context.Context) ([]probe.Reading, error) {
	if f.onCall != nil {
		f.onCall()
	}
	return f.readings, nil
}

func TestRunner_TickCallsAllProbers(t *testing.T) {
	var called []string
	p1 := &fakeProber{
		nameStr:  "a",
		readings: []probe.Reading{{Product: "x", Remaining: 80}},
		onCall:   func() { called = append(called, "a") },
	}
	p2 := &fakeProber{
		nameStr:  "b",
		readings: []probe.Reading{{Product: "y", Remaining: 50}},
		onCall:   func() { called = append(called, "b") },
	}

	alerts := make([]string, 0)
	notify := func(msg string) { alerts = append(alerts, msg) }

	r := &Runner{
		probers: []probe.Prober{p1, p2},
		notify:  notify,
		states:  map[string]*vendorState{"a": {}, "b": {}},
	}

	r.Tick(context.Background())

	if !slices.Equal(called, []string{"a", "b"}) {
		t.Fatalf("want [a b], got %v", called)
	}
	// No alerts for 80%/50%.
	if len(alerts) != 0 {
		t.Errorf("expected no alerts for healthy quota, got %v", alerts)
	}
}

func TestRunner_WarnAlert(t *testing.T) {
	p := &stubProber{vendor: "webshare", readings: []probe.Reading{{Product: "bandwidth", Remaining: 15}}}
	alerts := make([]string, 0)
	r := &Runner{
		probers: []probe.Prober{p},
		notify:  func(msg string) { alerts = append(alerts, msg) },
		states:  map[string]*vendorState{"webshare": {}},
	}

	r.Tick(context.Background())

	if len(alerts) != 1 {
		t.Fatalf("expected 1 warn alert, got %d: %v", len(alerts), alerts)
	}
}

func TestRunner_PageAlert(t *testing.T) {
	p := &stubProber{vendor: "webshare", readings: []probe.Reading{{Product: "bandwidth", Remaining: 3}}}
	alerts := make([]string, 0)
	r := &Runner{
		probers: []probe.Prober{p},
		notify:  func(msg string) { alerts = append(alerts, msg) },
		states:  map[string]*vendorState{"webshare": {}},
	}

	r.Tick(context.Background())

	if len(alerts) != 1 {
		t.Fatalf("expected 1 page alert, got %d: %v", len(alerts), alerts)
	}
}

func TestRunner_AlertDedup(t *testing.T) {
	p := &stubProber{vendor: "webshare", readings: []probe.Reading{{Product: "bandwidth", Remaining: 15}}}
	alerts := make([]string, 0)
	r := &Runner{
		probers: []probe.Prober{p},
		notify:  func(msg string) { alerts = append(alerts, msg) },
		states:  map[string]*vendorState{"webshare": {}},
	}

	// Tick multiple times at the same level — should alert only once.
	r.Tick(context.Background())
	r.Tick(context.Background())
	r.Tick(context.Background())

	if len(alerts) != 1 {
		t.Errorf("expected dedup to 1 alert, got %d: %v", len(alerts), alerts)
	}
}

func TestRunner_EscalatesFromWarnToPage(t *testing.T) {
	p := &stubProber{vendor: "webshare", readings: []probe.Reading{{Product: "bandwidth", Remaining: 15}}}
	alerts := make([]string, 0)
	r := &Runner{
		probers: []probe.Prober{p},
		notify:  func(msg string) { alerts = append(alerts, msg) },
		states:  map[string]*vendorState{"webshare": {}},
	}

	r.Tick(context.Background()) // warn at 15%

	// Drop to 3% — should escalate to page.
	p.readings = []probe.Reading{{Product: "bandwidth", Remaining: 3}}
	r.Tick(context.Background())

	if len(alerts) != 2 {
		t.Errorf("expected 2 alerts (warn + page), got %d: %v", len(alerts), alerts)
	}
}

func TestRunner_NoAlertWhenHealthy(t *testing.T) {
	p := &stubProber{vendor: "webshare", readings: []probe.Reading{{Product: "bandwidth", Remaining: 80}}}
	alerts := make([]string, 0)
	r := &Runner{
		probers: []probe.Prober{p},
		notify:  func(msg string) { alerts = append(alerts, msg) },
		states:  map[string]*vendorState{"webshare": {}},
	}

	r.Tick(context.Background())

	if len(alerts) != 0 {
		t.Errorf("expected no alerts for 80%% quota, got: %v", alerts)
	}
}

func TestRunner_Enabled_Empty(t *testing.T) {
	cfg := Config{}
	if cfg.Enabled() {
		t.Error("expected Enabled()=false for empty config")
	}
}

func TestRunner_Enabled_WithKey(t *testing.T) {
	cfg := Config{WebshareAPIKey: "key"}
	if !cfg.Enabled() {
		t.Error("expected Enabled()=true when WebshareAPIKey set")
	}
}
