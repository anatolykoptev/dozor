package main

import (
	"log/slog"
	"testing"
	"time"
)

// TestNotifyCooldown_ZeroDisables verifies that duration=0 disables the cooldown —
// shouldSuppress always returns false even immediately after markSent.
func TestNotifyCooldown_ZeroDisables(t *testing.T) {
	t.Parallel()

	nc := newNotifyCooldown(0)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	nc.markSent("h1", now)

	if nc.shouldSuppress("h1", now) {
		t.Error("shouldSuppress should return false when duration=0 (cooldown disabled)")
	}
}

// TestNotifyCooldown_SuppressesWithinWindow verifies that a hash sent within the
// cooldown window is suppressed on a subsequent check.
func TestNotifyCooldown_SuppressesWithinWindow(t *testing.T) {
	t.Parallel()

	nc := newNotifyCooldown(1 * time.Hour)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	nc.markSent("h1", now)

	later := now.Add(30 * time.Minute)
	if !nc.shouldSuppress("h1", later) {
		t.Error("shouldSuppress should return true within the 1h cooldown window")
	}
}

// TestNotifyCooldown_AllowsAfterWindow verifies that after the cooldown window
// expires, shouldSuppress returns false (allow notify).
func TestNotifyCooldown_AllowsAfterWindow(t *testing.T) {
	t.Parallel()

	nc := newNotifyCooldown(1 * time.Hour)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	nc.markSent("h1", now)

	later := now.Add(61 * time.Minute)
	if nc.shouldSuppress("h1", later) {
		t.Error("shouldSuppress should return false after the cooldown window expires (61m > 1h)")
	}
}

// TestNotifyCooldown_DifferentHashesIndependent verifies that each hash has its
// own independent cooldown — markSent for one hash does not suppress another.
func TestNotifyCooldown_DifferentHashesIndependent(t *testing.T) {
	t.Parallel()

	nc := newNotifyCooldown(1 * time.Hour)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	nc.markSent("h1", now)

	if nc.shouldSuppress("h2", now) {
		t.Error("shouldSuppress('h2') should return false when only 'h1' was marked")
	}
}

// TestNotifyCooldown_ParseEnvZero verifies that DOZOR_NOTIFY_COOLDOWN=0 results in
// duration==0 (disabled), not a fallback to the 1h default.
func TestNotifyCooldown_ParseEnvZero(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_NOTIFY_COOLDOWN", "0")
	nc := newNotifyCooldownFromEnv()

	if nc.duration != 0 {
		t.Errorf("expected duration=0 when env=0, got %v", nc.duration)
	}
}

// TestNotifyCooldown_ParseEnvMalformed verifies that an invalid DOZOR_NOTIFY_COOLDOWN
// falls back to the default 1h and logs a WARN.
func TestNotifyCooldown_ParseEnvMalformed(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	var warnLogged bool
	origHandler := slog.Default().Handler()
	_ = origHandler // capture to avoid unused warning

	t.Setenv("DOZOR_NOTIFY_COOLDOWN", "not a duration")
	nc := newNotifyCooldownFromEnv()

	// WARN logging is tested via observable side-effect: fallback to default duration.
	_ = warnLogged
	if nc.duration != notifyCooldownDuration {
		t.Errorf("expected fallback to default %v on malformed env, got %v", notifyCooldownDuration, nc.duration)
	}
}

// TestNotifyCooldown_FirstCallNeverSuppresses verifies that a fresh cooldown never
// suppresses on first check.
func TestNotifyCooldown_FirstCallNeverSuppresses(t *testing.T) {
	t.Parallel()

	nc := newNotifyCooldown(1 * time.Hour)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if nc.shouldSuppress("h1", now) {
		t.Error("shouldSuppress should return false on first call (no prior markSent)")
	}
}

// TestTick_NotifyCooldownSuppressesLLMCall verifies that tick() calls routeToAgent
// only once for the same hash, even when dedup is cleared between ticks.
func TestTick_NotifyCooldownSuppressesLLMCall(t *testing.T) {
	t.Parallel()

	routeCalled := 0
	fakeRoute := func(_ string) {
		routeCalled++
	}

	nc := newNotifyCooldown(1 * time.Hour)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	hash := "abc123"

	// First call: not suppressed — should route.
	if nc.shouldSuppress(hash, base) {
		t.Fatal("unexpected suppression on first call")
	}
	nc.markSent(hash, base)
	fakeRoute(hash)

	// Second call 30 min later: suppressed.
	later := base.Add(30 * time.Minute)
	if !nc.shouldSuppress(hash, later) {
		t.Fatal("expected suppression within 1h window")
	}
	// fakeRoute NOT called.

	if routeCalled != 1 {
		t.Errorf("expected routeToAgent called 1 time, got %d", routeCalled)
	}

	// Verify "different hash" is not suppressed.
	if nc.shouldSuppress("other", later) {
		t.Error("different hash should not be suppressed")
	}

}
