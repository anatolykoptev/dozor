package main

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestExtractIssues_LevelPerService asserts ExtractIssues attaches the correct
// per-line level to each issue — the level recovery that the deleted
// extractIssueLevel helper used to do by re-scanning the report. Each line owns
// its own service token, so there is no cross-service prefix collision (the old
// "go-hully vs go-hully-worker" hazard): go-hully-worker is ERROR, go-hully is
// CRITICAL, and they stay distinct.
func TestExtractIssues_LevelPerService(t *testing.T) {
	t.Parallel()

	report := `Server Triage Report
Health: critical | Time: 2026-02-23 12:00

[CRITICAL] go-hully — running, 1 restarts, 5 errors
[ERROR] go-hully-worker — running, 0 restarts, 2 errors
[WARNING] qdrant — running, 0 restarts, 5 errors`

	want := map[string]engine.AlertLevel{
		"go-hully":        engine.AlertCritical,
		"go-hully-worker": engine.AlertError,
		"qdrant":          engine.AlertWarning,
	}
	issues := engine.ExtractIssues(report)
	if len(issues) != len(want) {
		t.Fatalf("want %d issues, got %d: %+v", len(want), len(issues), issues)
	}
	for _, iss := range issues {
		if w, ok := want[iss.Service]; !ok || iss.Level != w {
			t.Errorf("issue %q: Level=%q, want %q", iss.Service, iss.Level, w)
		}
	}
}

func TestBuildAutoRemediateMessage_RestartedOnly(t *testing.T) {
	msg := buildAutoRemediateMessage([]string{"ox-whisper", "memdb-api"}, nil, nil)

	if !strings.Contains(msg, "Auto-fix applied") {
		t.Error("missing header")
	}
	if !strings.Contains(msg, "ox-whisper, memdb-api") {
		t.Error("missing restarted services")
	}
	if !strings.Contains(msg, "all services recovered") {
		t.Error("missing recovery confirmation")
	}
	if strings.Contains(msg, "Suppressed") {
		t.Error("should not contain Suppressed section")
	}
}

func TestBuildAutoRemediateMessage_SuppressedOnly(t *testing.T) {
	msg := buildAutoRemediateMessage(nil, []string{"qdrant (telemetry errors)", "searxng (rate limits)"}, nil)

	if !strings.Contains(msg, "Suppressed") {
		t.Error("missing Suppressed section")
	}
	if !strings.Contains(msg, "qdrant (telemetry errors)") {
		t.Error("missing qdrant suppression reason")
	}
	if strings.Contains(msg, "Restarted") {
		t.Error("should not contain Restarted section")
	}
}

func TestBuildAutoRemediateMessage_Both(t *testing.T) {
	msg := buildAutoRemediateMessage(
		[]string{"ox-whisper"},
		[]string{"qdrant (telemetry errors)"},
		nil,
	)

	if !strings.Contains(msg, "Restarted") {
		t.Error("missing Restarted section")
	}
	if !strings.Contains(msg, "Suppressed") {
		t.Error("missing Suppressed section")
	}
}

func TestWatchSuppressWarnings_KnownServices(t *testing.T) {
	// Use the default config (no DOZOR_SUPPRESS_WARNINGS env var set) to verify defaults.
	cfg := engine.Init()
	suppressWarnings := cfg.SuppressWarnings

	expected := map[string]bool{
		"qdrant":   true,
		"searxng":  true,
		"go-hully": true,
	}

	for svc := range expected {
		if _, ok := suppressWarnings[svc]; !ok {
			t.Errorf("service %q should be in SuppressWarnings by default", svc)
		}
	}

	// Make sure critical services are NOT in the suppress list
	for _, svc := range []string{"postgres", "redis", "memdb-api", "memdb-go"} {
		if _, ok := suppressWarnings[svc]; ok {
			t.Errorf("critical service %q should NOT be in SuppressWarnings", svc)
		}
	}
}
