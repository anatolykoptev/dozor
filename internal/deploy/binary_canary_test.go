package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// startSmokeServer starts an HTTP test server that returns the given status code.
// statusCode can be swapped atomically via the returned pointer.
func startSmokeServer(t *testing.T, initialCode int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var code atomic.Int32
	code.Store(int32(initialCode))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	t.Cleanup(srv.Close)
	return srv, &code
}

// stubSystemctl replaces systemctlRunner with a fake and returns a restore func.
//
// onRestart is called with the service name each time "--user restart <svc>" is seen.
// isActive returns false until the caller arranges otherwise; stub always reports "active\n".
func stubSystemctl(
	t *testing.T,
	onRestart func(svc string),
) func() {
	t.Helper()
	orig := systemctlRunner
	systemctlRunner = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[1] == "restart" {
			svc := args[2]
			if onRestart != nil {
				onRestart(svc)
			}
			return nil, nil
		}
		if len(args) >= 3 && args[1] == "is-active" {
			return []byte("active\n"), nil
		}
		return nil, nil
	}
	return func() { systemctlRunner = orig }
}

// TestCanaryDeploy_HappyPath: 3 services, smoke returns 200 — canary passes,
// remaining 2 are restarted AFTER the smoke window.
func TestCanaryDeploy_HappyPath(t *testing.T) {
	srv, _ := startSmokeServer(t, http.StatusOK)

	var restartOrder []string
	var restartTimes []time.Time
	restore := stubSystemctl(t, func(svc string) {
		restartOrder = append(restartOrder, svc)
		restartTimes = append(restartTimes, time.Now())
	})
	defer restore()

	cfg := RepoConfig{
		UserServices:       []string{"svc-a", "svc-b", "svc-c"},
		SmokeURL:           srv.URL,
		CanarySmokeTimeout: Duration{D: 2 * time.Second},
		CanarySmokeWindow:  Duration{D: 100 * time.Millisecond}, // short window for test speed
	}

	ctx := context.Background()
	if err := restartWithCanary(ctx, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// canary must be first
	if len(restartOrder) < 3 {
		t.Fatalf("expected 3 restarts, got %d", len(restartOrder))
	}
	if restartOrder[0] != "svc-a" {
		t.Errorf("expected canary svc-a first, got %s", restartOrder[0])
	}

	// svc-b and svc-c must appear after svc-a
	canaryTime := restartTimes[0]
	for i, svc := range restartOrder[1:] {
		if restartTimes[i+1].Before(canaryTime) {
			t.Errorf("service %s restarted BEFORE canary svc-a", svc)
		}
	}

	// svc-b and svc-c both present
	remaining := map[string]bool{"svc-b": false, "svc-c": false}
	for _, s := range restartOrder[1:] {
		remaining[s] = true
	}
	for svc, seen := range remaining {
		if !seen {
			t.Errorf("expected %s to be restarted but it was not", svc)
		}
	}
}

// TestCanaryDeploy_CanaryFailsRollback: smoke URL returns 500 — only canary
// restarted, remaining 2 services NOT touched, error returned.
func TestCanaryDeploy_CanaryFailsRollback(t *testing.T) {
	srv, _ := startSmokeServer(t, http.StatusInternalServerError)

	restarted := map[string]int{}
	restore := stubSystemctl(t, func(svc string) {
		restarted[svc]++
	})
	defer restore()

	cfg := RepoConfig{
		UserServices:       []string{"svc-canary", "svc-b", "svc-c"},
		SmokeURL:           srv.URL,
		CanarySmokeTimeout: Duration{D: 300 * time.Millisecond}, // fail fast
		CanarySmokeWindow:  Duration{D: 30 * time.Second},
	}

	ctx := context.Background()
	err := restartWithCanary(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when smoke returns 500, got nil")
	}

	// Only canary must have been restarted.
	if restarted["svc-canary"] == 0 {
		t.Error("canary service should have been restarted")
	}
	if restarted["svc-b"] != 0 {
		t.Errorf("svc-b should NOT have been restarted, got %d restart(s)", restarted["svc-b"])
	}
	if restarted["svc-c"] != 0 {
		t.Errorf("svc-c should NOT have been restarted, got %d restart(s)", restarted["svc-c"])
	}
}

// TestCanaryDeploy_SingleService_NoCanary: only one service — it is restarted
// and smoke-tested; no second stage.
func TestCanaryDeploy_SingleService_NoCanary(t *testing.T) {
	srv, _ := startSmokeServer(t, http.StatusOK)

	var restarted []string
	restore := stubSystemctl(t, func(svc string) {
		restarted = append(restarted, svc)
	})
	defer restore()

	cfg := RepoConfig{
		UserServices:       []string{"svc-only"},
		SmokeURL:           srv.URL,
		CanarySmokeTimeout: Duration{D: 2 * time.Second},
		CanarySmokeWindow:  Duration{D: 100 * time.Millisecond},
	}

	ctx := context.Background()
	if err := restartWithCanary(ctx, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(restarted) != 1 || restarted[0] != "svc-only" {
		t.Errorf("expected exactly one restart of svc-only, got %v", restarted)
	}
}

// TestCanaryDeploy_SmokeWindowFails: canary gets initial 200 but then drops
// to 500 during the sustain window — should return error.
func TestCanaryDeploy_SmokeWindowFails(t *testing.T) {
	srv, code := startSmokeServer(t, http.StatusOK)

	// After a short delay, flip to 500 to simulate regression during window.
	go func() {
		time.Sleep(60 * time.Millisecond)
		code.Store(http.StatusInternalServerError)
	}()

	restarted := map[string]int{}
	restore := stubSystemctl(t, func(svc string) {
		restarted[svc]++
	})
	defer restore()

	cfg := RepoConfig{
		UserServices:       []string{"svc-canary", "svc-b"},
		SmokeURL:           srv.URL,
		CanarySmokeTimeout: Duration{D: 2 * time.Second},
		CanarySmokeWindow:  Duration{D: 500 * time.Millisecond}, // long enough to flip
	}

	ctx := context.Background()
	err := restartWithCanary(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when smoke drops to 500 during window, got nil")
	}

	// svc-b must NOT have been restarted.
	if restarted["svc-b"] != 0 {
		t.Errorf("svc-b should NOT have been restarted, got %d restart(s)", restarted["svc-b"])
	}
}

// TestCanaryDeploy_NoSmokeURL: no smoke_url configured — canary restart still
// happens, remaining services restart after, no smoke check.
func TestCanaryDeploy_NoSmokeURL(t *testing.T) {
	var restarted []string
	restore := stubSystemctl(t, func(svc string) {
		restarted = append(restarted, svc)
	})
	defer restore()

	cfg := RepoConfig{
		UserServices: []string{"svc-a", "svc-b"},
		SmokeURL:     "", // no smoke
	}

	ctx := context.Background()
	if err := restartWithCanary(ctx, cfg); err != nil {
		t.Fatalf("unexpected error with no smoke_url: %v", err)
	}

	if len(restarted) != 2 {
		t.Errorf("expected 2 restarts, got %d: %v", len(restarted), restarted)
	}
	if restarted[0] != "svc-a" {
		t.Errorf("expected svc-a first, got %s", restarted[0])
	}
}

// TestDuration_OrDefault: zero value returns fallback, set value returns self.
func TestDuration_OrDefault(t *testing.T) {
	var zero Duration
	if got := zero.OrDefault(10 * time.Second); got != 10*time.Second {
		t.Errorf("zero.OrDefault(10s) = %v, want 10s", got)
	}

	d := Duration{D: 5 * time.Second}
	if got := d.OrDefault(10 * time.Second); got != 5*time.Second {
		t.Errorf("Duration{5s}.OrDefault(10s) = %v, want 5s", got)
	}
}
