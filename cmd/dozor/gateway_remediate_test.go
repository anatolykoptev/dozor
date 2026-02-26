package main

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

func TestExtractIssueLevel(t *testing.T) {
	triageResult := `Server Triage Report
Health: critical | Time: 2026-02-23 12:00

Services needing attention (3):

[CRITICAL] moonshine — exited, 0 restarts, 0 errors
[WARNING] qdrant — running, 0 restarts, 5 errors
  Issue: telemetry connection errors (5 occurrences)
[ERROR] memdb-api — running, 2 restarts, 3 errors
`

	tests := []struct {
		service string
		want    string
	}{
		{"moonshine", "CRITICAL"},
		{"qdrant", "WARNING"},
		{"memdb-api", "ERROR"},
		{"nonexistent", ""},
	}

	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			got := extractIssueLevel(triageResult, tt.service)
			if got != tt.want {
				t.Errorf("extractIssueLevel(%q) = %q, want %q", tt.service, got, tt.want)
			}
		})
	}
}

func TestExtractIssueLevel_ServiceNamePrefix(t *testing.T) {
	// Ensure "go-hully" doesn't match "go-hully-worker" line
	triageResult := `[WARNING] go-hully-worker — running, 0 restarts, 2 errors
[ERROR] go-hully — running, 1 restarts, 5 errors`

	if got := extractIssueLevel(triageResult, "go-hully"); got != "ERROR" {
		t.Errorf("expected ERROR for go-hully, got %q", got)
	}
}

func TestBuildAutoRemediateMessage_RestartedOnly(t *testing.T) {
	msg := buildAutoRemediateMessage([]string{"moonshine", "memdb-api"}, nil)

	if !strings.Contains(msg, "Auto-fix applied") {
		t.Error("missing header")
	}
	if !strings.Contains(msg, "moonshine, memdb-api") {
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
	msg := buildAutoRemediateMessage(nil, []string{"qdrant (telemetry errors)", "searxng (rate limits)"})

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
		[]string{"moonshine"},
		[]string{"qdrant (telemetry errors)"},
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
