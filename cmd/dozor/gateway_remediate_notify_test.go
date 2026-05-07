package main

import (
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// buildDiskResult constructs a DiskRemediateResult for test scenarios.
// freedMBPerTarget is the per-target freed amount in MB (uniform for simplicity).
func buildDiskResult(targetFreeds map[string]string, errs []string) *engine.DiskRemediateResult {
	res := &engine.DiskRemediateResult{}
	for name, freed := range targetFreeds {
		res.Targets = append(res.Targets, engine.CleanupTarget{
			Name:      name,
			Available: true,
			Freed:     freed,
		})
	}
	res.Errors = errs
	return res
}

// TestNotifyAutoRemediate_SkippedWhenAllZero verifies that when all targets report
// 0.0 MB freed and there are no errors, the notify callback is NOT called.
func TestNotifyAutoRemediate_SkippedWhenAllZero(t *testing.T) {
	t.Parallel()

	notifyCalled := false
	notify := func(_ string) { notifyCalled = true }

	res := buildDiskResult(map[string]string{
		"journal": "0.0 MB",
		"tmp":     "0.0 MB",
		"caches":  "0.0 MB",
	}, nil)

	shouldNotify := diskRemediateShouldNotify(res)
	if shouldNotify {
		notify("should not be called")
	}

	if notifyCalled {
		t.Error("notify was called even though all targets freed 0 MB and no errors — expected suppressed")
	}
}

// TestNotifyAutoRemediate_SentWhenFreedAboveThreshold verifies that when total freed
// is above the minimum threshold, the notify callback IS called.
func TestNotifyAutoRemediate_SentWhenFreedAboveThreshold(t *testing.T) {
	t.Parallel()

	notifyCalled := false
	notify := func(_ string) { notifyCalled = true }

	// 200 MB freed total — above 50 MB threshold.
	res := buildDiskResult(map[string]string{
		"journal": "100.0 MB",
		"tmp":     "50.0 MB",
		"caches":  "50.0 MB",
	}, nil)

	shouldNotify := diskRemediateShouldNotify(res)
	if shouldNotify {
		notify("triggered")
	}

	if !notifyCalled {
		t.Error("notify was NOT called even though total freed = 200 MB (above 50 MB threshold)")
	}
}

// TestNotifyAutoRemediate_SentWhenErrorsPresent verifies that even if nothing was freed,
// the notify path fires when errors are present (operator must see failures).
func TestNotifyAutoRemediate_SentWhenErrorsPresent(t *testing.T) {
	t.Parallel()

	notifyCalled := false
	notify := func(_ string) { notifyCalled = true }

	res := buildDiskResult(map[string]string{
		"journal": "0.0 MB",
		"tmp":     "0.0 MB",
		"caches":  "0.0 MB",
	}, []string{"caches: permission denied"})

	shouldNotify := diskRemediateShouldNotify(res)
	if shouldNotify {
		notify("triggered by errors")
	}

	if !notifyCalled {
		t.Error("notify was NOT called even though errors present — operator must see failures")
	}
}

// TestNotifyAutoRemediate_PartialFreed_BelowThreshold verifies that 20 MB freed (< 50 MB)
// is suppressed when no errors.
func TestNotifyAutoRemediate_PartialFreed_BelowThreshold(t *testing.T) {
	t.Parallel()

	notifyCalled := false
	notify := func(_ string) { notifyCalled = true }

	res := buildDiskResult(map[string]string{
		"journal": "20.0 MB",
		"tmp":     "0.0 MB",
		"caches":  "0.0 MB",
	}, nil)

	shouldNotify := diskRemediateShouldNotify(res)
	if shouldNotify {
		notify("triggered")
	}

	if notifyCalled {
		t.Error("notify was called for 20 MB freed (below 50 MB threshold) with no errors — expected suppressed")
	}
}
