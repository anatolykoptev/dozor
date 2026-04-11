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

// TestRateLimitPatternNoFalsePositiveOnNanoseconds is a regression guard for
// the previous bug where the unbounded `429` regex matched the substring `429`
// inside nanosecond timestamps like `073474290Z`, causing harmless 200 OK
// gin_logger lines to be reported as "Rate limiting triggered".
func TestRateLimitPatternNoFalsePositiveOnNanoseconds(t *testing.T) {
	// These lines all contain `429` as a substring of a longer number, but
	// none of them are actual rate-limit signals.
	benignLines := []string{
		`2026-04-11T09:18:37.073474290Z [info] [gin_logger.go:93] 200 |  7.847s | 172.18.0.10 | POST /v1/chat/completions`,
		`2026-04-11 17:42:90 [info] connection from 10.0.4.29 succeeded`,
		`processing batch 4290 of 5000`,
	}
	for _, line := range benignLines {
		entries := []LogEntry{{Level: "INFO", Message: line, Raw: line}}
		result := AnalyzeLogs(entries, "test-service")
		for _, issue := range result.Issues {
			if issue.Category == "rate_limit" {
				t.Errorf("rate-limit false positive on benign line:\n  %q\n  → matched as %q", line, issue.Description)
			}
		}
	}

	// Real rate-limit indicators should still trigger.
	realLines := []string{
		`HTTP/1.1 429 Too Many Requests`,
		`upstream returned 429 too many requests`,
		`rate limit exceeded for client`,
		`{"status": 429, "error": "throttled"}`,
		`got rate-limited by api`,
	}
	for _, line := range realLines {
		entries := []LogEntry{{Level: "ERROR", Message: line, Raw: line}}
		result := AnalyzeLogs(entries, "test-service")
		matched := false
		for _, issue := range result.Issues {
			if issue.Category == "rate_limit" {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("expected rate-limit pattern to match real signal, missed: %q", line)
		}
	}
}

// TestNoiseSuppression_Cliproxyapi verifies that round-robin LLM key rotation
// 502s are surfaced as known noise (with reason) rather than as issues.
func TestNoiseSuppression_Cliproxyapi(t *testing.T) {
	entries := []LogEntry{
		{
			Level:   "INFO",
			Message: `[2026-04-11 17:23:14] [41041d24] [error] [gin_logger.go:89] 502 |           1ms |     172.18.0.10 | POST    "/v1/chat/completions"`,
			Raw:     `[2026-04-11 17:23:14] [41041d24] [error] [gin_logger.go:89] 502 |           1ms |     172.18.0.10 | POST    "/v1/chat/completions"`,
		},
		{
			Level:   "INFO",
			Message: `[2026-04-11 17:26:58] [0e6fc151] [error] [gin_logger.go:89] 502 |            0s |     172.18.0.10 | POST    "/v1/chat/completions"`,
			Raw:     `[2026-04-11 17:26:58] [0e6fc151] [error] [gin_logger.go:89] 502 |            0s |     172.18.0.10 | POST    "/v1/chat/completions"`,
		},
	}
	result := AnalyzeLogs(entries, "cliproxyapi")
	if result.NoiseCount != 2 {
		t.Errorf("expected NoiseCount=2, got %d", result.NoiseCount)
	}
	if len(result.NoiseHits) != 1 {
		t.Errorf("expected 1 noise reason group, got %d", len(result.NoiseHits))
	}
	if len(result.NoiseHits) > 0 {
		if result.NoiseHits[0].Count != 2 {
			t.Errorf("expected count=2 in noise hit, got %d", result.NoiseHits[0].Count)
		}
		if !strings.Contains(result.NoiseHits[0].Reason, "round-robin") {
			t.Errorf("expected reason to mention round-robin, got: %q", result.NoiseHits[0].Reason)
		}
	}
	// And no fake "issue" should be raised from these lines.
	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues from suppressed lines, got %d: %+v", len(result.Issues), result.Issues)
	}
}

// TestNoiseSuppression_NotForOtherService confirms that the cliproxyapi noise
// rule does not silently swallow lines for OTHER services that happen to look
// similar — service scoping must be enforced.
func TestNoiseSuppression_NotForOtherService(t *testing.T) {
	entries := []LogEntry{
		{
			Level:   "INFO",
			Message: `gin_logger.go:89] 502 |   1ms | 172.18.0.10 | POST "/v1/chat/completions"`,
			Raw:     `gin_logger.go:89] 502 |   1ms | 172.18.0.10 | POST "/v1/chat/completions"`,
		},
	}
	result := AnalyzeLogs(entries, "some-other-service")
	if result.NoiseCount != 0 {
		t.Errorf("expected NoiseCount=0 for non-cliproxyapi service, got %d", result.NoiseCount)
	}
}

// TestNoiseSuppression_Cloakbrowser checks the ARM Chromium init noise filter.
func TestNoiseSuppression_Cloakbrowser(t *testing.T) {
	entries := []LogEntry{
		{
			Level:   "ERROR",
			Message: `[19:19:0411/021042.803514:ERROR:chrome/browser/ui/views/user_education/impl/browser_user_education_interface_impl.cc:154] Attempting to show IPH IPH_ExtensionsZeroStatePromo before browser initialization complete`,
			Raw:     `[19:19:0411/021042.803514:ERROR:chrome/browser/ui/views/user_education/impl/browser_user_education_interface_impl.cc:154] Attempting to show IPH IPH_ExtensionsZeroStatePromo before browser initialization complete`,
		},
		{
			Level:   "ERROR",
			Message: `[46:46:0411/015357.097748:ERROR:gpu/command_buffer/service/shared_image/shared_image_manager.cc:252] SharedImageManager::ProduceSkia: Trying to Produce a Skia representation from a non-existent mailbox.`,
			Raw:     `[46:46:0411/015357.097748:ERROR:gpu/command_buffer/service/shared_image/shared_image_manager.cc:252] SharedImageManager::ProduceSkia: Trying to Produce a Skia representation from a non-existent mailbox.`,
		},
	}
	result := AnalyzeLogs(entries, "cloakbrowser")
	if result.NoiseCount != 2 {
		t.Errorf("expected NoiseCount=2, got %d", result.NoiseCount)
	}
	// Importantly: ERROR-level lines that match noise should NOT count toward ErrorCount.
	if result.ErrorCount != 0 {
		t.Errorf("noise lines must not increment ErrorCount, got ErrorCount=%d", result.ErrorCount)
	}
}
