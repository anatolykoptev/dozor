package engine

import (
	"strings"
	"testing"
)

// TestExtractIssues_ParsesLevel asserts ExtractIssues now keeps the [LEVEL]
// marker it previously discarded, exposing it as TriageIssue.Level. Recovering
// the level by re-scanning the report (the old extractIssueLevel helper) is no
// longer necessary.
func TestExtractIssues_ParsesLevel(t *testing.T) {
	t.Parallel()

	report := "[CRITICAL] ox-whisper — exited\n" +
		"[ERROR] memdb-api — 2 restarts\n" +
		"[WARNING_HIGH] disk — /dev/sda1 at 88%\n" +
		"[WARNING] qdrant — 5 errors"

	issues := ExtractIssues(report)
	if len(issues) != 4 {
		t.Fatalf("want 4 issues, got %d: %+v", len(issues), issues)
	}

	want := map[string]AlertLevel{
		"ox-whisper": AlertCritical,
		"memdb-api":  AlertError,
		"disk":       AlertWarningHigh,
		"qdrant":     AlertWarning,
	}
	for _, iss := range issues {
		if w, ok := want[iss.Service]; !ok || iss.Level != w {
			t.Errorf("issue %q: Level=%q, want %q", iss.Service, iss.Level, w)
		}
	}
}

// TestFormatIssueLine_Canonical asserts the single emitter produces a line that
// ExtractIssues round-trips: emit -> parse -> same service/level/description.
func TestFormatIssueLine_Canonical(t *testing.T) {
	t.Parallel()

	cases := []struct {
		level   AlertLevel
		service string
		desc    string
		token   string
	}{
		{AlertCritical, "postgres", "exited with code 1", "[CRITICAL]"},
		{AlertError, "memdb-api", "connection refused", "[ERROR]"},
		{AlertWarningHigh, "disk", "/dev/sda1 at 88%", "[WARNING_HIGH]"},
		{AlertWarning, "redis", "3 restarts", "[WARNING]"},
	}
	for _, tc := range cases {
		line := FormatIssueLine(tc.level, tc.service, tc.desc)
		if !strings.HasPrefix(line, tc.token+" "+tc.service+TriageMachineSep) {
			t.Errorf("FormatIssueLine(%q,%q,%q) = %q, missing canonical prefix",
				tc.level, tc.service, tc.desc, line)
		}
		// Round-trip through the canonical parser.
		issues := ExtractIssues(line)
		if len(issues) != 1 {
			t.Fatalf("FormatIssueLine output not parseable by ExtractIssues: %q -> %+v", line, issues)
		}
		if issues[0].Service != tc.service || issues[0].Level != tc.level {
			t.Errorf("round-trip mismatch: got service=%q level=%q, want %q/%q",
				issues[0].Service, issues[0].Level, tc.service, tc.level)
		}
	}
}

// TestMachineToken maps every actionable AlertLevel to its uppercase report
// token; AlertInfo has no machine marker (ExtractIssues does not parse [INFO]).
func TestMachineToken(t *testing.T) {
	t.Parallel()

	cases := map[AlertLevel]string{
		AlertCritical:    "CRITICAL",
		AlertError:       "ERROR",
		AlertWarningHigh: "WARNING_HIGH",
		AlertWarning:     "WARNING",
		AlertInfo:        "",
	}
	for level, want := range cases {
		if got := level.MachineToken(); got != want {
			t.Errorf("%q.MachineToken() = %q, want %q", level, got, want)
		}
	}
}

// TestAlertIssueLine_LLMDistinctService asserts two different LLM proxy
// failures map to DISTINCT canonical service names so the dedup hash keeps
// them apart. Before unification both carried Service="llm" and collapsed:
// a second, different LLM failure within the cooldown window was suppressed.
func TestAlertIssueLine_LLMDistinctService(t *testing.T) {
	t.Parallel()

	a := Alert{Level: AlertWarning, Service: "llm", Title: "LLM proxy gemini-3.1: rate limited (HTTP 429)", Description: "quota exceeded"}
	b := Alert{Level: AlertError, Service: "llm", Title: "LLM proxy qwen-3-235b: auth failure (HTTP 401)", Description: "bad key"}

	lineA := AlertIssueLine(a)
	lineB := AlertIssueLine(b)

	issA := ExtractIssues(lineA)
	issB := ExtractIssues(lineB)
	if len(issA) != 1 || len(issB) != 1 {
		t.Fatalf("AlertIssueLine output must be a single parseable issue each: %q / %q", lineA, lineB)
	}
	if issA[0].Service == issB[0].Service {
		t.Errorf("distinct LLM failures must yield distinct services, both = %q", issA[0].Service)
	}
	if issA[0].Level != AlertWarning || issB[0].Level != AlertError {
		t.Errorf("levels lost: A=%q B=%q", issA[0].Level, issB[0].Level)
	}
}

// TestAlertIssueLine_RemoteNamespaced asserts remote alerts get a stable,
// remote-namespaced service so two different remote entities stay distinct.
func TestAlertIssueLine_RemoteNamespaced(t *testing.T) {
	t.Parallel()

	a := Alert{Level: AlertCritical, Service: "https://a.example", Title: "Site unreachable", Description: "no response"}
	b := Alert{Level: AlertError, Service: "nginx", Title: "Remote service nginx is failed", Description: "not active"}

	issA := ExtractIssues(AlertIssueLine(a))
	issB := ExtractIssues(AlertIssueLine(b))
	if len(issA) != 1 || len(issB) != 1 {
		t.Fatalf("remote alert lines must each parse to one issue")
	}
	for _, iss := range []TriageIssue{issA[0], issB[0]} {
		if !strings.HasPrefix(iss.Service, "remote:") {
			t.Errorf("remote alert service must be namespaced, got %q", iss.Service)
		}
	}
	if issA[0].Service == issB[0].Service {
		t.Errorf("distinct remote entities must stay distinct, both = %q", issA[0].Service)
	}
}

// TestFormatLLMAlerts_CanonicalLines asserts the LLM alert formatter now emits
// canonical, ExtractIssues-parseable lines (not the old "- [LEVEL] title: desc"
// shape that was invisible to the parser).
func TestFormatLLMAlerts_CanonicalLines(t *testing.T) {
	t.Parallel()

	alerts := []Alert{
		{Level: AlertWarning, Service: "llm", Title: "LLM proxy gemini-3.1: rate limited (HTTP 429)", Description: "quota exceeded"},
		{Level: AlertError, Service: "llm", Title: "Gemini key AIzaSyCcP...: invalid (HTTP 403)", Description: "PERMISSION_DENIED"},
	}
	out := FormatLLMAlerts(alerts)

	issues := ExtractIssues(out)
	if len(issues) != 2 {
		t.Fatalf("FormatLLMAlerts must emit 2 first-class issues, got %d from:\n%s", len(issues), out)
	}
	if issues[0].Service == issues[1].Service {
		t.Errorf("two distinct LLM alerts collapsed to one service: %q", issues[0].Service)
	}
}
