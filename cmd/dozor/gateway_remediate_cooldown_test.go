package main

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestCooldown_BlocksSecondRunWithinWindow verifies that a second shouldSkip call
// within the cooldown window returns true (skip).
func TestCooldown_BlocksSecondRunWithinWindow(t *testing.T) {
	t.Parallel()

	cd := newRemediateCooldown(30 * time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cd.markRun("disk", "warning", now)

	// 5 minutes later — still inside 30-min window.
	later := now.Add(5 * time.Minute)
	if !cd.shouldSkip("disk", "warning", later) {
		t.Error("shouldSkip should return true within the cooldown window")
	}
}

// TestCooldown_AllowsRunAfterWindow verifies that after the cooldown window expires,
// shouldSkip returns false (allow run).
func TestCooldown_AllowsRunAfterWindow(t *testing.T) {
	t.Parallel()

	cd := newRemediateCooldown(30 * time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cd.markRun("disk", "warning", now)

	// 31 minutes later — outside window.
	later := now.Add(31 * time.Minute)
	if cd.shouldSkip("disk", "warning", later) {
		t.Error("shouldSkip should return false after the cooldown window expires")
	}
}

// TestCooldown_PerServicePerLevel verifies that cooldown is tracked independently
// per (service, level) pair — markRun for one level should not block another level.
func TestCooldown_PerServicePerLevel(t *testing.T) {
	t.Parallel()

	cd := newRemediateCooldown(30 * time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cd.markRun("disk", "warning", now)

	// Same service, different level — should NOT be blocked.
	if cd.shouldSkip("disk", "critical", now) {
		t.Error("shouldSkip for 'disk:critical' should return false when only 'disk:warning' was marked")
	}

	// Different service, same level — should NOT be blocked.
	if cd.shouldSkip("memory", "warning", now) {
		t.Error("shouldSkip for 'memory:warning' should return false when only 'disk:warning' was marked")
	}
}

// TestCooldown_FirstCallNeverSkips verifies that a fresh cooldown never blocks.
func TestCooldown_FirstCallNeverSkips(t *testing.T) {
	t.Parallel()

	cd := newRemediateCooldown(30 * time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if cd.shouldSkip("disk", "warning", now) {
		t.Error("shouldSkip should return false on first call (no prior run)")
	}
}

// TestTryAutoRemediate_SkipsWhenCooldownActive verifies the end-to-end path:
// when a cooldown is active for the disk service+level, AutoRemediateDisk is
// only called once across two consecutive calls to handleDiskIssueWithCooldown.
func TestTryAutoRemediate_SkipsWhenCooldownActive(t *testing.T) {
	t.Parallel()

	callCount := 0
	stub := &countingDiskRemediator{
		onCall: func() *engine.DiskRemediateResult {
			callCount++
			return &engine.DiskRemediateResult{
				Targets: []engine.CleanupTarget{
					{Name: "caches", Freed: "200.0 MB", FreedMB: 200},
				},
			}
		},
	}

	cd := newRemediateCooldown(30 * time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	issue := engine.TriageIssue{Service: "disk", Description: "disk at 88%"}

	// First call — cooldown not active, should run.
	handleDiskIssueWithCooldown(context.Background(), stub, issue, "WARNING", cd, now)

	// Second call — 2 minutes later, cooldown still active.
	later := now.Add(2 * time.Minute)
	handleDiskIssueWithCooldown(context.Background(), stub, issue, "WARNING", cd, later)

	if callCount != 1 {
		t.Errorf("AutoRemediateDisk called %d times, expected exactly 1 (second call should be suppressed by cooldown)", callCount)
	}
}

// TestCooldown_ZeroDisables verifies that duration=0 disables the cooldown — shouldSkip
// always returns false even immediately after markRun.
func TestCooldown_ZeroDisables(t *testing.T) {
	t.Parallel()

	cd := newRemediateCooldown(0)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cd.markRun("disk", "warning", now)

	// Immediately after markRun — should NOT skip when duration is 0.
	if cd.shouldSkip("disk", "warning", now) {
		t.Error("shouldSkip should return false when duration=0 (cooldown disabled)")
	}
}

// TestCooldown_ParseEnvZero verifies that DOZOR_REMEDIATE_COOLDOWN=0s results in
// duration==0 (disabled), not a fallback to the 30m default.
func TestCooldown_ParseEnvZero(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_REMEDIATE_COOLDOWN", "0s")
	cd := newRemediateCooldownFromEnv()

	if cd.duration != 0 {
		t.Errorf("expected duration=0 when env=0s, got %v", cd.duration)
	}
}

// TestCooldown_ParseEnvMalformed verifies that an invalid DOZOR_REMEDIATE_COOLDOWN
// falls back to the default 30m.
func TestCooldown_ParseEnvMalformed(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_REMEDIATE_COOLDOWN", "abc")
	cd := newRemediateCooldownFromEnv()

	if cd.duration != remediateCooldownDuration {
		t.Errorf("expected fallback to default %v on malformed env, got %v", remediateCooldownDuration, cd.duration)
	}
}

// countingDiskRemediator counts AutoRemediateDisk calls.
type countingDiskRemediator struct {
	onCall func() *engine.DiskRemediateResult
}

func (c *countingDiskRemediator) AutoRemediateDisk(_ context.Context, _ engine.AlertLevel) *engine.DiskRemediateResult {
	return c.onCall()
}
