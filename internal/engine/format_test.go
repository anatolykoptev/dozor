package engine

import (
	"strings"
	"testing"
	"time"
)

func TestFormatAnalysis(t *testing.T) {
	result := AnalyzeResult{
		Service:      "test-svc",
		TotalLines:   100,
		ErrorCount:   5,
		WarningCount: 2,
		Issues: []Issue{
			{Level: AlertCritical, Category: "database", Description: "Connection failure", Action: "Check DB", Count: 3, Example: "FATAL: auth failed"},
		},
	}
	output := FormatAnalysis(result)
	if !strings.Contains(output, "test-svc") {
		t.Error("expected service name")
	}
	if !strings.Contains(output, "100") {
		t.Error("expected total lines")
	}
	if !strings.Contains(output, "Connection failure") {
		t.Error("expected issue description")
	}
	if !strings.Contains(output, "3 occurrences") {
		t.Error("expected occurrence count")
	}
}

func TestFormatAnalysisNoIssues(t *testing.T) {
	result := AnalyzeResult{
		Service:    "clean-svc",
		TotalLines: 50,
	}
	output := FormatAnalysis(result)
	if !strings.Contains(output, "No known error patterns") {
		t.Error("expected no patterns message")
	}
}

func TestFormatAnalysisEnriched(t *testing.T) {
	now := time.Now()
	entries := []LogEntry{
		{Level: "ERROR", Message: "connection refused from 1.2.3.4", Timestamp: &now},
		{Level: "ERROR", Message: "connection refused from 5.6.7.8", Timestamp: &now},
	}
	result := AnalyzeLogs(entries, "test-svc")
	output := FormatAnalysisEnriched(result, entries)

	if !strings.Contains(output, "Error Timeline") {
		t.Error("expected Error Timeline section")
	}
	if !strings.Contains(output, "Top Error Clusters") {
		t.Error("expected Error Clusters section")
	}
}

func TestFormatSecurityReport(t *testing.T) {
	t.Run("no issues", func(t *testing.T) {
		output := FormatSecurityReport(nil)
		if !strings.Contains(output, "No issues") {
			t.Error("expected no issues message")
		}
	})

	t.Run("with issues", func(t *testing.T) {
		issues := []SecurityIssue{
			{Level: AlertCritical, Category: "network", Title: "Port exposed", Description: "PostgreSQL port open", Remediation: "Bind to 127.0.0.1"},
			{Level: AlertWarning, Category: "ssh", Title: "Root login", Description: "PermitRootLogin yes", Remediation: "Disable root login"},
		}
		output := FormatSecurityReport(issues)
		if !strings.Contains(output, "2 issue(s)") {
			t.Error("expected issue count")
		}
		if !strings.Contains(output, "NETWORK") {
			t.Error("expected NETWORK category header")
		}
		if !strings.Contains(output, "Fix:") {
			t.Error("expected Fix: remediation line")
		}
	})

	t.Run("with evidence", func(t *testing.T) {
		issues := []SecurityIssue{
			{Level: AlertWarning, Category: "files", Title: "Perms", Description: "desc", Remediation: "fix", Evidence: "/path/to/.env"},
		}
		output := FormatSecurityReport(issues)
		if !strings.Contains(output, "Evidence:") {
			t.Error("expected Evidence line")
		}
	})
}

func TestFormatProbeResultsWithHeaders(t *testing.T) {
	results := []ProbeResult{
		{
			URL: "https://example.com", Status: 200, OK: true, LatencyMs: 100,
			SecurityHeaders: &SecurityHeadersResult{
				HSTS:    "max-age=31536000",
				CSP:     "default-src 'self'",
				Missing: nil,
			},
		},
	}
	output := FormatProbeResults(results)
	if !strings.Contains(output, "Security headers [OK]") {
		t.Error("expected [OK] for all headers present")
	}
}
