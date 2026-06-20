package main

import (
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// deployLogSpies returns a makeDeployLog wired to two recorders: text captures
// plain-text notifyFn messages (✅ success / 🔨 build-start), cards captures the
// alert-card notifyAlertFn payloads (❌ failure / ⚠️ rollback). Failures render
// as deterministic cards now, so they no longer flow through the text path.
func deployLogSpies(mode string) (fn func(string), text *[]string, cards *[]engine.Alert) {
	var sentText []string
	var sentCards []engine.Alert
	fn = makeDeployLog(mode,
		func(msg string) { sentText = append(sentText, msg) },
		func(alerts []engine.Alert, _ string) { sentCards = append(sentCards, alerts...) },
	)
	return fn, &sentText, &sentCards
}

// TestMakeDeployLog_FailureModeDefault verifies that the default "failure" mode
// renders ❌/⚠️ messages as alert cards and silently drops ✅ success / 🔨 build.
// RED-on-revert: if the filter is removed, ✅ messages reach TG on default mode.
func TestMakeDeployLog_FailureModeDefault(t *testing.T) {
	t.Parallel()

	fn, text, cards := deployLogSpies("failure")

	fn("🔨 [my-svc] Building... (commit abc1234)")
	fn("✅ [my-svc] Deployed (1m23s)")
	fn("❌ [my-svc] FAILED: compose up exited 1")
	fn("⚠️ [my-svc] FAILED (rolled back): health check timeout")

	if len(*text) != 0 {
		t.Fatalf("failure mode must forward NO plain-text messages, got %d: %v", len(*text), *text)
	}
	if len(*cards) != 2 {
		t.Fatalf("failure mode must render exactly 2 failure cards, got %d", len(*cards))
	}
	if (*cards)[0].Level != engine.AlertCritical {
		t.Errorf("❌ failure card must be critical, got %q", (*cards)[0].Level)
	}
	if (*cards)[1].Level != engine.AlertWarning {
		t.Errorf("⚠️ rollback card must be warning, got %q", (*cards)[1].Level)
	}
	if (*cards)[0].Service != "deploy" {
		t.Errorf("card service must be deploy, got %q", (*cards)[0].Service)
	}
}

// TestMakeDeployLog_AllMode verifies that mode="all" emits ✅ success and 🔨
// build-start as plain text while ❌ failures still render as alert cards.
func TestMakeDeployLog_AllMode(t *testing.T) {
	t.Parallel()

	fn, text, cards := deployLogSpies("all")

	fn("🔨 [my-svc] Building... (commit abc1234)")
	fn("✅ [my-svc] Deployed (1m23s)")
	fn("❌ [my-svc] FAILED: timeout")

	if len(*text) != 2 {
		t.Fatalf("all mode must forward 2 plain-text messages (🔨 + ✅), got %d: %v", len(*text), *text)
	}
	if len(*cards) != 1 {
		t.Fatalf("all mode must render 1 failure card (❌), got %d", len(*cards))
	}
	if (*cards)[0].Level != engine.AlertCritical {
		t.Errorf("❌ failure card must be critical, got %q", (*cards)[0].Level)
	}
}

// TestMakeDeployLog_EmptyModeDefaultsToFailure verifies that an empty mode
// string behaves identically to "failure" (the caller normalises "" → "failure"
// but the function itself should handle empty gracefully too).
func TestMakeDeployLog_EmptyModeDefaultsToFailure(t *testing.T) {
	t.Parallel()

	fn, text, cards := deployLogSpies("")

	fn("✅ [my-svc] Deployed (30s)")
	fn("❌ [my-svc] FAILED: oom")

	if len(*text) != 0 {
		t.Fatalf("empty mode (failure) must forward NO plain text, got %d: %v", len(*text), *text)
	}
	if len(*cards) != 1 {
		t.Fatalf("empty mode (failure) must render only the ❌ card, got %d", len(*cards))
	}
	if (*cards)[0].Level != engine.AlertCritical {
		t.Errorf("expected critical ❌ card, got %q", (*cards)[0].Level)
	}
}
