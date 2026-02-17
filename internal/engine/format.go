package engine

import (
	"fmt"
	"strings"
	"time"
)

// FormatReport creates a human-readable diagnostic report.
func FormatReport(r DiagnosticReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Server Diagnostic Report\n")
	fmt.Fprintf(&b, "Server: %s | Time: %s | Health: %s\n\n",
		r.Server, r.Timestamp.Format("2006-01-02 15:04:05"), r.OverallHealth)

	fmt.Fprintf(&b, "Services (%d):\n", len(r.Services))
	for _, s := range r.Services {
		icon := "OK"
		if !s.IsHealthy() {
			icon = "!!"
		}
		fmt.Fprintf(&b, "  [%s] %s: %s", icon, s.Name, s.State)
		if s.CPUPercent != nil {
			fmt.Fprintf(&b, " | CPU: %.1f%%", *s.CPUPercent)
		}
		if s.MemoryMB != nil {
			fmt.Fprintf(&b, " | Mem: %.0fMB", *s.MemoryMB)
		}
		if s.RestartCount > 0 {
			fmt.Fprintf(&b, " | Restarts: %d", s.RestartCount)
		}
		if s.ErrorCount > 0 {
			fmt.Fprintf(&b, " | Errors: %d", s.ErrorCount)
		}
		b.WriteString("\n")
	}

	if len(r.Alerts) > 0 {
		fmt.Fprintf(&b, "\nAlerts (%d):\n", len(r.Alerts))
		for _, a := range r.Alerts {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", a.Level, a.Service, a.Title)
			fmt.Fprintf(&b, "    %s\n", a.Description)
			fmt.Fprintf(&b, "    Action: %s\n", a.SuggestedAction)
		}
	}

	return b.String()
}

// FormatStatus creates a human-readable status for a single service.
func FormatStatus(s ServiceStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Service: %s\n", s.Name)
	fmt.Fprintf(&b, "State: %s\n", s.State)
	if s.Health != "" {
		fmt.Fprintf(&b, "Health: %s\n", s.Health)
	}
	if s.Uptime != "" {
		fmt.Fprintf(&b, "Uptime: %s\n", s.Uptime)
	}
	fmt.Fprintf(&b, "Restarts: %d\n", s.RestartCount)
	if s.CPUPercent != nil {
		fmt.Fprintf(&b, "CPU: %.1f%%\n", *s.CPUPercent)
	}
	if s.MemoryMB != nil {
		fmt.Fprintf(&b, "Memory: %.0f MB\n", *s.MemoryMB)
	}
	fmt.Fprintf(&b, "Errors: %d\n", s.ErrorCount)
	return b.String()
}

// FormatScanResults formats cleanup scan results as a human-readable report.
func FormatScanResults(results []CleanupTarget) string {
	var b strings.Builder
	b.WriteString("System Cleanup Scan\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	var totalMB float64
	for _, r := range results {
		if !r.Available {
			fmt.Fprintf(&b, "  [--] %-10s not available\n", r.Name)
			continue
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "  [!!] %-10s error: %s\n", r.Name, r.Error)
			continue
		}
		if r.SizeMB >= 1024 {
			fmt.Fprintf(&b, "  [OK] %-10s %.1f GB\n", r.Name, r.SizeMB/1024)
		} else {
			fmt.Fprintf(&b, "  [OK] %-10s %.0f MB\n", r.Name, r.SizeMB)
		}
		totalMB += r.SizeMB
	}

	b.WriteString("\n")
	if totalMB >= 1024 {
		fmt.Fprintf(&b, "Total reclaimable: %.1f GB\n", totalMB/1024)
	} else {
		fmt.Fprintf(&b, "Total reclaimable: %.0f MB\n", totalMB)
	}
	b.WriteString("Run server_cleanup({report: false}) to execute cleanup.\n")
	return b.String()
}

// FormatCleanResults formats cleanup execution results.
func FormatCleanResults(results []CleanupTarget) string {
	var b strings.Builder
	b.WriteString("System Cleanup Results\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	for _, r := range results {
		if !r.Available {
			fmt.Fprintf(&b, "  [--] %-10s not available\n", r.Name)
			continue
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "  [!!] %-10s error: %s\n", r.Name, r.Error)
			continue
		}
		freed := r.Freed
		if freed == "" {
			freed = "0 MB"
		}
		fmt.Fprintf(&b, "  [OK] %-10s freed %s\n", r.Name, freed)
	}
	return b.String()
}

// FormatRemoteStatus formats remote server status as a human-readable report.
func FormatRemoteStatus(s *RemoteServerStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Remote Server: %s\n", s.Host)
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	if s.HTTPStatus > 0 {
		icon := "OK"
		if s.HTTPStatus >= 400 {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] HTTP: %d\n", icon, s.HTTPStatus)
	}

	if s.SSLExpiry != nil {
		days := int(time.Until(*s.SSLExpiry).Hours() / 24)
		icon := "OK"
		if days < 14 {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] SSL expires: %s (%d days)\n", icon, s.SSLExpiry.Format("2006-01-02"), days)
	}

	if len(s.Services) > 0 {
		b.WriteString("\nServices:\n")
		for name, state := range s.Services {
			icon := "OK"
			if state != "active" {
				icon = "!!"
			}
			fmt.Fprintf(&b, "  [%s] %s: %s\n", icon, name, state)
		}
	}

	if s.DiskUsage != "" {
		fmt.Fprintf(&b, "\nDisk: %s\n", s.DiskUsage)
	}
	if s.LoadAvg != "" {
		fmt.Fprintf(&b, "Load: %s\n", s.LoadAvg)
	}

	if len(s.Alerts) > 0 {
		fmt.Fprintf(&b, "\nAlerts (%d):\n", len(s.Alerts))
		for _, a := range s.Alerts {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", a.Level, a.Service, a.Title)
		}
	}

	return b.String()
}

// FormatSecurityReport formats security issues as a human-readable report.
func FormatSecurityReport(issues []SecurityIssue) string {
	if len(issues) == 0 {
		return "Security Audit: No issues detected."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Security Audit: %d issue(s) found\n", len(issues))

	categories := make(map[string][]SecurityIssue)
	for _, issue := range issues {
		categories[issue.Category] = append(categories[issue.Category], issue)
	}

	for cat, catIssues := range categories {
		fmt.Fprintf(&b, "\n## %s\n", strings.ToUpper(cat))
		for _, issue := range catIssues {
			fmt.Fprintf(&b, "  [%s] %s\n", issue.Level, issue.Title)
			fmt.Fprintf(&b, "    %s\n", issue.Description)
			fmt.Fprintf(&b, "    Fix: %s\n", issue.Remediation)
			if issue.Evidence != "" {
				fmt.Fprintf(&b, "    Evidence: %s\n", issue.Evidence)
			}
		}
	}

	return b.String()
}

// FormatAnalysis returns a human-readable analysis report.
func FormatAnalysis(r AnalyzeResult) string {
	s := fmt.Sprintf("Log Analysis: %s\nTotal lines: %d | Errors: %d | Warnings: %d\n",
		r.Service, r.TotalLines, r.ErrorCount, r.WarningCount)

	if len(r.Issues) == 0 {
		s += "\nNo known error patterns detected."
		return s
	}

	s += fmt.Sprintf("\nDetected %d issue type(s):\n", len(r.Issues))
	for _, issue := range r.Issues {
		s += fmt.Sprintf("\n[%s] %s (%d occurrences)\n", issue.Level, issue.Description, issue.Count)
		s += fmt.Sprintf("  Category: %s\n", issue.Category)
		s += fmt.Sprintf("  Action: %s\n", issue.Action)
		if issue.Example != "" {
			s += fmt.Sprintf("  Example: %s\n", issue.Example)
		}
	}

	return s
}
