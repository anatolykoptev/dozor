package engine

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeErrorMessage(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			"connection refused from 192.168.1.100:5432",
			"connection refused from <IP>",
		},
		{
			"failed for user a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			"failed for user <UUID>",
		},
		{
			"pointer at 0xDEADBEEF crashed",
			"pointer at <HEX> crashed",
		},
		{
			"pid 12345 exited with code 137",
			"pid <N> exited with code <N>",
		},
		{
			"2026-02-20T15:29:22.516Z FATAL: role does not exist",
			"FATAL: role does not exist",
		},
		{
			"2026-02-20 15:29:22.516 UTC [17816] FATAL: auth failed",
			"UTC [<N>] FATAL: auth failed",
		},
		{
			// IP with port
			"connecting to 10.0.0.1:6379 failed",
			"connecting to <IP> failed",
		},
		{
			// Short message stays short
			"error",
			"error",
		},
	}
	for _, c := range cases {
		got := normalizeErrorMessage(c.input)
		if got != c.expected {
			t.Errorf("normalizeErrorMessage(%q)\n  got:  %q\n  want: %q", c.input, got, c.expected)
		}
	}
}

func TestNormalizeErrorMessageTruncation(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := normalizeErrorMessage(long)
	if len(got) > 120 {
		t.Errorf("expected truncation to 120 chars, got %d", len(got))
	}
}

func TestAnalyzeErrorTimelineNoErrors(t *testing.T) {
	entries := []LogEntry{
		{Level: "INFO", Message: "all good"},
	}
	result := AnalyzeErrorTimeline(entries)
	if !strings.Contains(result, "No errors") {
		t.Errorf("expected 'No errors' message, got: %s", result)
	}
}

func TestAnalyzeErrorTimelineWithErrors(t *testing.T) {
	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	twoHoursAgo := now.Add(-2 * time.Hour)

	entries := []LogEntry{
		{Level: "ERROR", Message: "err1", Timestamp: &oneHourAgo},
		{Level: "ERROR", Message: "err2", Timestamp: &oneHourAgo},
		{Level: "FATAL", Message: "fatal1", Timestamp: &twoHoursAgo},
		{Level: "INFO", Message: "not an error", Timestamp: &oneHourAgo},
	}
	result := AnalyzeErrorTimeline(entries)
	if !strings.Contains(result, "3 total") {
		t.Errorf("expected 3 total errors, got: %s", result)
	}
	if !strings.Contains(result, "█") {
		t.Errorf("expected histogram bars, got: %s", result)
	}
}

func TestAnalyzeErrorTimelineSkipsOld(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	entries := []LogEntry{
		{Level: "ERROR", Message: "old error", Timestamp: &old},
	}
	result := AnalyzeErrorTimeline(entries)
	if !strings.Contains(result, "No errors") {
		t.Errorf("expected old errors to be skipped, got: %s", result)
	}
}

func TestAnalyzeErrorTimelineNilTimestamp(t *testing.T) {
	entries := []LogEntry{
		{Level: "ERROR", Message: "no timestamp"},
	}
	result := AnalyzeErrorTimeline(entries)
	if !strings.Contains(result, "No errors") {
		t.Errorf("expected nil timestamps to be skipped, got: %s", result)
	}
}

func TestClusterErrors(t *testing.T) {
	now := time.Now()
	entries := []LogEntry{
		{Level: "ERROR", Message: "connection refused from 192.168.1.1:5432", Timestamp: &now},
		{Level: "ERROR", Message: "connection refused from 10.0.0.2:5432", Timestamp: &now},
		{Level: "ERROR", Message: "connection refused from 172.16.0.3:5432", Timestamp: &now},
		{Level: "ERROR", Message: "disk full on /dev/sda1", Timestamp: &now},
		{Level: "INFO", Message: "this is not an error", Timestamp: &now},
	}
	clusters := ClusterErrors(entries)
	if len(clusters) == 0 {
		t.Fatal("expected at least one cluster")
	}
	// The 3 connection refused errors should cluster together
	found := false
	for _, c := range clusters {
		if c.Count == 3 && strings.Contains(c.Template, "connection refused") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 3 'connection refused' errors to cluster, got: %+v", clusters)
	}
}

func TestClusterErrorsMaxFive(t *testing.T) {
	now := time.Now()
	var entries []LogEntry
	for i := 0; i < 10; i++ {
		entries = append(entries, LogEntry{
			Level:     "ERROR",
			Message:   strings.Repeat("x", i+1) + " unique error",
			Timestamp: &now,
		})
	}
	clusters := ClusterErrors(entries)
	if len(clusters) > 5 {
		t.Errorf("expected max 5 clusters, got %d", len(clusters))
	}
}

func TestClusterErrorsSortedByCount(t *testing.T) {
	now := time.Now()
	entries := []LogEntry{
		{Level: "ERROR", Message: "rare error alpha", Timestamp: &now},
		{Level: "ERROR", Message: "common error beta", Timestamp: &now},
		{Level: "ERROR", Message: "common error beta", Timestamp: &now},
		{Level: "ERROR", Message: "common error beta", Timestamp: &now},
	}
	clusters := ClusterErrors(entries)
	if len(clusters) < 2 {
		t.Fatal("expected at least 2 clusters")
	}
	if clusters[0].Count < clusters[1].Count {
		t.Error("clusters should be sorted by count descending")
	}
}

func TestClusterErrorsEmpty(t *testing.T) {
	clusters := ClusterErrors(nil)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for nil input, got %d", len(clusters))
	}
}

func TestFormatErrorClustersEmpty(t *testing.T) {
	result := FormatErrorClusters(nil)
	if !strings.Contains(result, "No error clusters") {
		t.Errorf("expected 'No error clusters', got: %s", result)
	}
}

func TestFormatErrorClusters(t *testing.T) {
	clusters := []ErrorCluster{
		{Template: "connection refused from <IP>", Count: 5, Example: "connection refused from 1.2.3.4"},
		{Template: "disk full", Count: 2, Example: "disk full on /dev/sda"},
	}
	result := FormatErrorClusters(clusters)
	if !strings.Contains(result, "Top Error Clusters (2)") {
		t.Errorf("expected header, got: %s", result)
	}
	if !strings.Contains(result, "[5x]") {
		t.Errorf("expected [5x] count, got: %s", result)
	}
	if !strings.Contains(result, "Example:") {
		t.Errorf("expected Example line, got: %s", result)
	}
}

func TestAnalyzeLogs(t *testing.T) {
	entries := []LogEntry{
		{Level: "ERROR", Message: "FATAL: authentication failed for user postgres"},
		{Level: "ERROR", Message: "connection refused"},
		{Level: "WARNING", Message: "timeout exceeded"},
		{Level: "INFO", Message: "normal operation"},
	}
	result := AnalyzeLogs(entries, "test-service")
	if result.ErrorCount != 2 {
		t.Errorf("expected 2 errors, got %d", result.ErrorCount)
	}
	if result.WarningCount != 1 {
		t.Errorf("expected 1 warning, got %d", result.WarningCount)
	}
	if len(result.Issues) == 0 {
		t.Error("expected at least one issue to be detected")
	}
}
