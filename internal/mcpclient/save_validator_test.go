package mcpclient

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSavePayload_Empty(t *testing.T) {
	for _, s := range []string{"", "   ", "\n\n\t", " \n\t "} {
		err := ValidateSavePayload(s)
		if err == nil {
			t.Errorf("expected empty-content rejection for %q", s)
		}
		if !errors.Is(err, ErrInvalidSavePayload) {
			t.Errorf("error should wrap ErrInvalidSavePayload, got %v", err)
		}
	}
}

func TestValidateSavePayload_RawDialog(t *testing.T) {
	cases := []string{
		"user: ALERT from external monitor (/webhook/monitor/healthcheck): TLS BLOCKED",
		"assistant: Ситуация осложнилась: внешние ресурсы недоступны.",
		"  user:   another chat export",
		"USER: uppercase still counts",
		"ASSISTANT: so does this",
	}
	for _, s := range cases {
		err := ValidateSavePayload(s)
		if err == nil {
			t.Errorf("expected dialog-prefix rejection for %q", s)
		}
		if !strings.Contains(err.Error(), "raw dialog log") {
			t.Errorf("reason should mention raw dialog log, got %q", err.Error())
		}
	}
}

func TestValidateSavePayload_NumericVitalWithoutCitation(t *testing.T) {
	cases := []string{
		"Server is in critical state: swap 99% used",
		"Memory pressure detected — RAM 92% utilised",
		"Load average: load 51.3 sustained for 10 minutes",
		"disk 95% full on /dev/sda1",
		"CPU at 88% across all cores",
	}
	for _, s := range cases {
		err := ValidateSavePayload(s)
		if err == nil {
			t.Errorf("expected numeric-without-citation rejection for %q", s)
		}
		if !strings.Contains(err.Error(), "numeric vital claim") {
			t.Errorf("reason should mention numeric vital claim, got %q", err.Error())
		}
	}
}

func TestValidateSavePayload_NumericVitalWithCitation(t *testing.T) {
	cases := []string{
		"Observed swap 45% via `free -h` output:\n  Swap: 8Gi 3.6Gi 4.4Gi",
		"load 51.3 from uptime: ` 03:30:15 up 10 days, load average: 51.3, 34.0, 23.9`",
		"Disk 87% on /dev/sda1 per df -h",
		"Memory at 92% — htop shows chrome processes dominating",
	}
	for _, s := range cases {
		if err := ValidateSavePayload(s); err != nil {
			t.Errorf("expected pass for cited numeric vital %q, got %v", s, err)
		}
	}
}

func TestValidateSavePayload_IncidentWithoutEvidence(t *testing.T) {
	cases := []string{
		"Incident: cloakbrowser GPU errors\nFix: restart",
		"Symptom: recurring timeouts\nRoot cause: network saturation",
		"Incident: postgres slow\nFix: restart pool\nPrevention: increase pool size",
	}
	for _, s := range cases {
		err := ValidateSavePayload(s)
		if err == nil {
			t.Errorf("expected missing-evidence rejection for %q", s)
		}
		if !strings.Contains(err.Error(), "Evidence:") {
			t.Errorf("reason should mention Evidence, got %q", err.Error())
		}
	}
}

func TestValidateSavePayload_IncidentWithEvidence(t *testing.T) {
	s := `Incident: cloakbrowser GPU errors on ARM
Evidence:
  [19:19:0411/022524:ERROR:chrome/browser/.../shared_image_manager.cc:252]
  docker ps: cloakbrowser Up 4 hours (healthy)
Root cause: ARM Chromium hardware probing — benign
Fix: none required — filter in dozor noiseRules
Prevention: keep the noiseRule in log_analyzer.go`

	if err := ValidateSavePayload(s); err != nil {
		t.Errorf("expected pass for incident-with-evidence, got %v", err)
	}
}

func TestValidateSavePayload_ShortFactPasses(t *testing.T) {
	cases := []string{
		"Postgres default user is `memos`, not `postgres`.",
		"Claude Code CLI does not use cliproxyapi — independent auth.",
		"tweets table lives in ag_catalog schema, not public.",
	}
	for _, s := range cases {
		if err := ValidateSavePayload(s); err != nil {
			t.Errorf("expected pass for short fact %q, got %v", s, err)
		}
	}
}
