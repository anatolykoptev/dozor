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

// TestIsNamespacedService asserts the predicate the remediation guard keys on:
// remote:/llm: prefixed services (written by alertReportService) are namespaced
// and therefore NOT remediable; bare local docker/systemd names are not. The
// prefixes match what AlertIssueLine actually emits, so the producer and the
// predicate cannot drift.
func TestIsNamespacedService(t *testing.T) {
	t.Parallel()

	namespaced := []string{
		"remote:https://a.example",
		"remote:nginx",
		"llm:LLM proxy gemini-3.1: rate limited (HTTP 429)",
		"llm:Gemini key AIzaSyCcP...: invalid (HTTP 403)",
	}
	for _, s := range namespaced {
		if !IsNamespacedService(s) {
			t.Errorf("IsNamespacedService(%q) = false, want true (remote/llm namespace is not remediable)", s)
		}
	}

	local := []string{"ox-whisper", "memdb-api", "disk", "go-hully", "postgres"}
	for _, s := range local {
		if IsNamespacedService(s) {
			t.Errorf("IsNamespacedService(%q) = true, want false (local docker/systemd service is remediable)", s)
		}
	}
}

// TestIsNamespacedService_MatchesEmitter asserts the predicate accepts every
// service name alertReportService can actually produce — the guard and the
// emitter share the prefix constants, so a new namespace added to the emitter
// is automatically covered by the guard.
func TestIsNamespacedService_MatchesEmitter(t *testing.T) {
	t.Parallel()

	alerts := []Alert{
		{Level: AlertCritical, Service: "https://x.example", Title: "Site unreachable"},
		{Level: AlertError, Service: "nginx", Title: "Remote service nginx is failed"},
		{Level: AlertWarning, Service: "llm", Title: "LLM proxy gemini-3.1: rate limited"},
	}
	for _, a := range alerts {
		svc := alertReportService(a)
		if !IsNamespacedService(svc) {
			t.Errorf("emitter produced %q but IsNamespacedService returned false — emitter/guard drift", svc)
		}
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

// TestAlertIssueLine_NoDuplication locks the fix for the operator-visible
// triple duplication: a proxy-model alert must render the model ONCE (in the
// service), not in the title and description again.
// Bug shape (2026-06-10 report #4bf4388c15fc7fad):
//
//	llm:LLM proxy gemini…: upstream error (HTTP 502) — llm:LLM proxy gemini…:
//	upstream error (HTTP 502): LLM proxy gemini…: upstream error (HTTP 502): {body}
func TestAlertIssueLine_NoDuplication(t *testing.T) {
	t.Parallel()

	a := Alert{
		Level:       AlertWarning,
		Service:     llmServicePrefix + "gemini-3.1-flash-lite-preview",
		Title:       "upstream error (HTTP 502)",
		Description: `{"error":{"message":"unknown provider"}}`,
	}
	got := AlertIssueLine(a)

	want := "[WARNING] llm:gemini-3.1-flash-lite-preview — upstream error (HTTP 502): " +
		`{"error":{"message":"unknown provider"}}` + "\n"
	if got != want {
		t.Errorf("AlertIssueLine =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(got, "gemini-3.1-flash-lite-preview"); n != 1 {
		t.Errorf("model name must appear exactly once, got %d times:\n%s", n, got)
	}
	if n := strings.Count(got, "upstream error"); n != 1 {
		t.Errorf("error kind must appear exactly once, got %d times:\n%s", n, got)
	}
}

// TestAlertIssueLine_StableServiceAcrossErrorKinds: the SAME entity failing
// with different HTTP codes must keep the same service name (dedup identity).
func TestAlertIssueLine_StableServiceAcrossErrorKinds(t *testing.T) {
	t.Parallel()

	a502 := Alert{Level: AlertWarning, Service: llmServicePrefix + "m1", Title: "upstream error (HTTP 502)"}
	a429 := Alert{Level: AlertWarning, Service: llmServicePrefix + "m1", Title: "rate limited (HTTP 429)"}

	i502 := ExtractIssues(AlertIssueLine(a502))
	i429 := ExtractIssues(AlertIssueLine(a429))
	if len(i502) != 1 || len(i429) != 1 {
		t.Fatalf("each line must parse to exactly one issue: %d, %d", len(i502), len(i429))
	}
	if i502[0].Service != i429[0].Service {
		t.Errorf("same entity must keep one dedup identity across error kinds: %q vs %q",
			i502[0].Service, i429[0].Service)
	}
}
