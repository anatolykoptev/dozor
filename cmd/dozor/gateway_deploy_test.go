package main

import (
	"strings"
	"testing"
)

// TestMakeDeployLog_FailureModeDefault verifies that the default "failure" mode
// forwards ❌/⚠️ messages to TG and silently drops ✅ success messages.
// RED-on-revert: if the filter is removed, ✅ messages reach TG on default mode.
func TestMakeDeployLog_FailureModeDefault(t *testing.T) {
	t.Parallel()

	var sent []string
	fn := makeDeployLog("failure", func(msg string) { sent = append(sent, msg) })

	fn("🔨 [my-svc] Building... (commit abc1234)")
	fn("✅ [my-svc] Deployed (1m23s)")
	fn("❌ [my-svc] FAILED: compose up exited 1")
	fn("⚠️ [my-svc] FAILED (rolled back): health check timeout")

	if len(sent) != 2 {
		t.Fatalf("failure mode must forward exactly 2 messages (failures only), got %d: %v", len(sent), sent)
	}
	for _, msg := range sent {
		if strings.HasPrefix(msg, "✅") || strings.HasPrefix(msg, "🔨") {
			t.Errorf("failure mode must not forward success/building messages, got: %s", msg)
		}
	}
	if !strings.HasPrefix(sent[0], "❌") {
		t.Errorf("first forwarded message must be the ❌ failure, got: %s", sent[0])
	}
	if !strings.HasPrefix(sent[1], "⚠️") {
		t.Errorf("second forwarded message must be the ⚠️ rollback, got: %s", sent[1])
	}
}

// TestMakeDeployLog_AllMode verifies that mode="all" forwards every message
// including ✅ success and 🔨 build-start.
func TestMakeDeployLog_AllMode(t *testing.T) {
	t.Parallel()

	var sent []string
	fn := makeDeployLog("all", func(msg string) { sent = append(sent, msg) })

	fn("🔨 [my-svc] Building... (commit abc1234)")
	fn("✅ [my-svc] Deployed (1m23s)")
	fn("❌ [my-svc] FAILED: timeout")

	if len(sent) != 3 {
		t.Fatalf("all mode must forward all 3 messages, got %d: %v", len(sent), sent)
	}
}

// TestMakeDeployLog_EmptyModeDefaultsToFailure verifies that an empty mode
// string behaves identically to "failure" (the caller normalises "" → "failure"
// but the function itself should handle empty gracefully too).
func TestMakeDeployLog_EmptyModeDefaultsToFailure(t *testing.T) {
	t.Parallel()

	var sent []string
	fn := makeDeployLog("", func(msg string) { sent = append(sent, msg) })

	fn("✅ [my-svc] Deployed (30s)")
	fn("❌ [my-svc] FAILED: oom")

	if len(sent) != 1 {
		t.Fatalf("empty mode (treated as failure) must forward only ❌, got %d: %v", len(sent), sent)
	}
	if !strings.HasPrefix(sent[0], "❌") {
		t.Errorf("expected ❌ message, got: %s", sent[0])
	}
}
