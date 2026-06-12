package main

import (
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestHashResult_DistinctLLMFailures is the regression guard for the dedup
// collapse bug: before unification every LLM alert carried Service="llm", so
// ExtractIssues found no issue at all for a pure-LLM failure (empty set ->
// single shared hash) and a DIFFERENT LLM failure within the cooldown window
// was silently suppressed. With LLM alerts now first-class, two distinct
// failures must produce two distinct hashes.
func TestHashResult_DistinctLLMFailures(t *testing.T) {
	t.Parallel()

	a := []engine.Alert{{Level: engine.AlertWarning, Service: "llm", Title: "LLM proxy gemini-3.1: rate limited (HTTP 429)", Description: "quota"}}
	b := []engine.Alert{{Level: engine.AlertError, Service: "llm", Title: "LLM proxy qwen-3-235b: auth failure (HTTP 401)", Description: "bad key"}}

	reportA := "Server Triage Report\nHealth: degraded |\n\n" + engine.FormatLLMAlerts(a)
	reportB := "Server Triage Report\nHealth: degraded |\n\n" + engine.FormatLLMAlerts(b)

	if hashResult(reportA) == hashResult(reportB) {
		t.Fatalf("distinct pure-LLM failures must hash differently; both = %q", hashResult(reportA))
	}
	// And each must hash to something other than the empty-issue-set hash.
	if hashResult(reportA) == hashResult("Health: degraded |\nno issues") {
		t.Error("a real LLM failure must not collapse to the empty-issue hash")
	}
}

// TestBuildMechanicalReport_LLMAlertIsFirstClass replaces the deleted
// TestBuildMechanicalReport_ExtraAlertFallback. The fallback (rawAlertLines)
// is gone: a pure-LLM failure now parses as a named TriageIssue and renders as
// a first-class "<code>service</code> — desc" line, with severity ranked from
// the parsed level.
func TestBuildMechanicalReport_LLMAlertIsFirstClass(t *testing.T) {
	t.Parallel()

	alerts := []engine.Alert{{Level: engine.AlertError, Service: "llm", Title: "LLM proxy gemini-3.1-flash-lite: auth failure (HTTP 401)", Description: "invalid key"}}
	result := "Health: degraded\n\n" + engine.FormatLLMAlerts(alerts)

	issues := engine.ExtractIssues(result)
	if len(issues) != 1 {
		t.Fatalf("LLM failure must be a first-class issue now, got %d issues from:\n%s", len(issues), result)
	}

	got := buildMechanicalReport(issues, "cafe0001", time.Now())

	if strings.Contains(got, "<b>Issues (0):</b>") {
		t.Errorf("LLM failure must not render an empty issue list:\n%s", got)
	}
	if !strings.Contains(got, "<code>") {
		t.Errorf("LLM failure must render as a named <code>service</code> line:\n%s", got)
	}
	if !strings.Contains(got, "<b>Status:</b> degraded") {
		t.Errorf("severity must rank from the parsed [ERROR] level:\n%s", got)
	}
}

// TestReportSeverity_RanksExtraAlerts asserts severity ranking now works
// uniformly over docker triage lines AND extra-alert (LLM/remote) lines,
// because they share the canonical format. A pure-LLM ERROR ranks "degraded".
func TestReportSeverity_RanksExtraAlerts(t *testing.T) {
	t.Parallel()

	alerts := []engine.Alert{{Level: engine.AlertError, Service: "llm", Title: "LLM proxy x: auth failure", Description: "bad"}}
	result := "Health: degraded\n\n" + engine.FormatLLMAlerts(alerts)
	issues := engine.ExtractIssues(result)

	if got := reportSeverity(issues); got != "degraded" {
		t.Errorf("reportSeverity over a pure-LLM ERROR = %q, want %q", got, "degraded")
	}
}
