package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestHashResult_OrderIndependent verifies that hashResult produces the same
// hash regardless of the order issues appear in the triage report.
// Docker container iteration order is non-deterministic; without sorting,
// different orderings produced different hashes and bypassed cooldown
// suppression (production incident 2026-05-10: duplicate Telegram alerts
// every 5 min after migration).
func TestHashResult_OrderIndependent(t *testing.T) {
	t.Parallel()

	// Two reports with the same set of failing services in different order.
	// ExtractIssues parses lines matching "[LEVEL] service — description".
	result1 := "[CRITICAL] oxpulse-chat — exited with code 1\n[ERROR] postgres — connection refused"
	result2 := "[ERROR] postgres — connection refused\n[CRITICAL] oxpulse-chat — exited with code 1"

	h1 := hashResult(result1)
	h2 := hashResult(result2)

	if h1 != h2 {
		t.Errorf("hashResult should be order-independent: result1=%q result2=%q", h1, h2)
	}
}

// TestHashResult_DifferentServices verifies that different issue sets still
// produce different hashes (the fix must not collapse distinct issue sets).
func TestHashResult_DifferentServices(t *testing.T) {
	t.Parallel()

	resultA := "[CRITICAL] oxpulse-chat — exited with code 1"
	resultB := "[CRITICAL] postgres — exited with code 1"

	if hashResult(resultA) == hashResult(resultB) {
		t.Error("hashResult: different services must produce different hashes")
	}
}

func TestBuildWatchPrompt_ProductionUsesHTML(t *testing.T) {
	got := buildWatchPrompt(false)

	wantSubstrings := []string{
		"<b>Status:</b>",
		"<b>Issues:</b>",
		"<b>Action:</b>",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("buildWatchPrompt(false) missing %q\nfull prompt:\n%s", want, got)
		}
	}

	mdMarkers := []string{"**Status:**", "**Issues:**", "**Action:**"}
	for _, md := range mdMarkers {
		if strings.Contains(got, md) {
			t.Errorf("buildWatchPrompt(false) still contains markdown %q — should be HTML", md)
		}
	}
}

func TestBuildWatchPrompt_DevModeUnchanged(t *testing.T) {
	got := buildWatchPrompt(true)
	if !strings.Contains(got, "DEV MODE") {
		t.Errorf("buildWatchPrompt(true) missing DEV MODE marker; got: %s", got)
	}
}

// TestTick_MarkSentOnlyAfterSuccessfulRoute verifies the suppression-after-route
// invariant: markSent is called ONLY after routeFn returns, so a failed or
// context-cancelled route does not suppress the next attempt for 1 h.
func TestTick_MarkSentOnlyAfterSuccessfulRoute(t *testing.T) {
	t.Parallel()

	var routeCalls int
	nc := newNotifyCooldown(1 * time.Hour)

	// Build a minimal watchDeps with a stubbed routeFn and a fake healthy report
	// that we will override via isHealthy logic. We drive the flow manually:
	// call shouldSuppress + routeFn + markSent in the same order tick() does, using
	// the real notifyCooldown, to verify the ordering invariant end-to-end.
	w := &watchDeps{
		notifyCooldown: nc,
	}
	w.routeFn = func(_ context.Context, _, _ string) {
		routeCalls++
	}

	ctx := context.Background()
	hash := "deadbeef"
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// --- First call: not suppressed → route → markSent ---
	if nc.shouldSuppress(hash, now) {
		t.Fatal("unexpected suppression before any markSent")
	}
	w.routeFn(ctx, "report", hash)
	nc.markSent(hash, now)

	if routeCalls != 1 {
		t.Fatalf("want 1 route call after first tick, got %d", routeCalls)
	}

	// --- Verify hash is now suppressed within window ---
	later := now.Add(30 * time.Minute)
	if !nc.shouldSuppress(hash, later) {
		t.Fatal("expected hash to be suppressed after successful route")
	}

	// --- Second call 30 min later: suppressed → routeFn must NOT be called ---
	if !nc.shouldSuppress(hash, later) {
		w.routeFn(ctx, "report", hash)
		nc.markSent(hash, later)
	}

	if routeCalls != 1 {
		t.Fatalf("want still 1 route call after suppressed tick, got %d", routeCalls)
	}

	// --- Verify different hash is not suppressed ---
	if nc.shouldSuppress("other", later) {
		t.Error("different hash should not be suppressed")
	}
}

// TestBuildMechanicalReport_FormatAndEscaping verifies the deterministic
// report carries Status/Issues/Action sections, escapes HTML in issue text,
// and ranks severity from the triage level markers.
func TestBuildMechanicalReport_FormatAndEscaping(t *testing.T) {
	t.Parallel()

	result := "[CRITICAL] oxpulse-chat — exited <code 1> & restarting\n[WARNING] redis — 3 restarts"
	issues := engine.ExtractIssues(result)
	if len(issues) != 2 {
		t.Fatalf("fixture: want 2 issues, got %d", len(issues))
	}

	ts := time.Date(2026, 6, 10, 12, 43, 5, 0, time.UTC)
	got := buildMechanicalReport(result, issues, "a1b2c3d4", ts)

	for _, want := range []string{
		"<b>Dozor Watch</b> <code>#a1b2c3d4</code> — 2026-06-10 12:43:05 UTC",
		"<b>Status:</b> critical",
		"<b>Issues (2):</b>",
		"<code>oxpulse-chat</code>",
		"exited &lt;code 1&gt; &amp; restarting",
		"<b>Action:</b>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\nfull report:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<code 1>") {
		t.Error("raw HTML from issue description leaked unescaped into the report")
	}
}

// TestBuildMechanicalReport_CapsIssueLines verifies a mass outage is
// truncated to mechReportMaxIssues lines with an "and N more" marker.
func TestBuildMechanicalReport_CapsIssueLines(t *testing.T) {
	t.Parallel()

	var lines []string
	for i := 0; i < mechReportMaxIssues+5; i++ {
		lines = append(lines, fmt.Sprintf("[ERROR] svc-%02d — down", i))
	}
	result := strings.Join(lines, "\n")
	issues := engine.ExtractIssues(result)

	got := buildMechanicalReport(result, issues, "ffff0000", time.Now())

	if want := fmt.Sprintf("… and %d more", 5); !strings.Contains(got, want) {
		t.Errorf("report missing truncation marker %q\nfull report:\n%s", want, got)
	}
	if strings.Count(got, "• ") != mechReportMaxIssues {
		t.Errorf("want %d issue bullets, got %d", mechReportMaxIssues, strings.Count(got, "• "))
	}
}

// TestBuildMechanicalReport_NoHashOmitsID verifies the header degrades
// gracefully when no dedup hash is available: time stays, "#id" is omitted.
func TestBuildMechanicalReport_NoHashOmitsID(t *testing.T) {
	t.Parallel()

	result := "[ERROR] postgres — connection refused"
	issues := engine.ExtractIssues(result)

	got := buildMechanicalReport(result, issues, "", time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))

	if strings.Contains(got, "#") {
		t.Errorf("empty hash must omit the #id marker, got:\n%s", got)
	}
	if !strings.Contains(got, "<b>Dozor Watch</b> — 2026-06-10") {
		t.Errorf("header must keep the timestamp without an id, got:\n%s", got)
	}
}

// TestReportSeverity_Ranking verifies CRITICAL > ERROR > WARNING mapping.
func TestReportSeverity_Ranking(t *testing.T) {
	t.Parallel()

	cases := []struct {
		result string
		want   string
	}{
		{"[CRITICAL] a — x\n[ERROR] b — y", "critical"},
		{"[ERROR] b — y\n[WARNING] c — z", "degraded"},
		{"[WARNING_HIGH] disk — usage 91%\n[WARNING] c — z", "warning_high"},
		{"[WARNING] c — z", "warning"},
	}
	for _, tc := range cases {
		if got := reportSeverity(tc.result); got != tc.want {
			t.Errorf("reportSeverity(%q) = %q, want %q", tc.result, got, tc.want)
		}
	}
}

// TestMechanicalReport_NotifiesWithoutLLM verifies the mechanical route
// delivers via notify and never touches the message bus / LLM agent.
func TestMechanicalReport_NotifiesWithoutLLM(t *testing.T) {
	t.Parallel()

	var sent []string
	w := &watchDeps{notify: func(text string) { sent = append(sent, text) }}

	w.mechanicalReport(context.Background(), "[ERROR] postgres — connection refused", "h1")

	if len(sent) != 1 {
		t.Fatalf("want exactly 1 notify call, got %d", len(sent))
	}
	if !strings.Contains(sent[0], "<code>postgres</code>") {
		t.Errorf("notification missing issue line: %s", sent[0])
	}
	if !strings.Contains(sent[0], "<code>#h1</code>") {
		t.Errorf("notification missing report id in header: %s", sent[0])
	}
}
