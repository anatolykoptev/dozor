package main

import (
	"context"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// stubDiskRemediator implements diskRemediator for test scenarios.
type stubDiskRemediator struct {
	result *engine.DiskRemediateResult
}

func (s *stubDiskRemediator) AutoRemediateDisk(_ context.Context, _ engine.AlertLevel) *engine.DiskRemediateResult {
	return s.result
}

// TestSumDiskFreedMB_NonStandardFormats verifies that sumDiskFreedMB uses the typed
// FreedMB field and correctly totals non-standard Freed string formats that previously
// caused silent parse failures (docker "1.2 GB (4 images)", memory "500 MB (drop caches)").
func TestSumDiskFreedMB_NonStandardFormats(t *testing.T) {
	t.Parallel()

	res := &engine.DiskRemediateResult{
		Targets: []engine.CleanupTarget{
			{Name: "docker", Freed: "1.2 GB (4 images)", FreedMB: 1228.8},
			{Name: "memory", Freed: "500 MB (drop caches)", FreedMB: 500},
			{Name: "caches", Freed: "50 MB (3 dirs)", FreedMB: 50},
		},
	}

	got := sumDiskFreedMB(res)
	want := 1778.8

	// Allow 0.01 epsilon for float rounding.
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("sumDiskFreedMB: got %.2f MB, want %.2f MB — non-standard Freed strings must not cause silent zero-count", got, want)
	}
}

// TestHandleDiskIssue_NotifiesOnErrors verifies that when disk cleanup returns errors,
// handleDiskIssue returns (non-empty message, true) so the notify path fires.
// Previously it returned ("", false) → issue landed in unhandled[] → notify never called.
func TestHandleDiskIssue_NotifiesOnErrors(t *testing.T) {
	t.Parallel()

	stub := &stubDiskRemediator{
		result: &engine.DiskRemediateResult{
			Targets: []engine.CleanupTarget{
				{Name: "journal", Freed: "0.0 MB", FreedMB: 0},
			},
			Errors: []string{"journalctl failed: permission denied"},
		},
	}

	issue := engine.TriageIssue{Service: "disk", Level: engine.AlertCritical, Description: "disk at 91%"}
	msg, handled := handleDiskIssueWith(context.Background(), stub, issue, engine.AlertCritical)

	if !handled {
		t.Error("handleDiskIssue: handled=false even though cleanup returned errors — issue lands in unhandled[], notify never fires")
	}
	if !strings.Contains(msg, "journalctl failed") {
		t.Errorf("handleDiskIssue: message should contain error text, got: %q", msg)
	}
}
