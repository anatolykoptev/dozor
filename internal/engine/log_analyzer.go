package engine

import (
	"fmt"
	"regexp"
)

var errorPatterns = []ErrorPattern{
	// Database errors (generic — works with any SQL database)
	{
		Pattern:         `(?i)(FATAL|authentication failed|could not connect to server|password authentication failed)`,
		Level:           AlertCritical,
		Category:        "database",
		Description:     "Database authentication or connection failure",
		SuggestedAction: "Check database credentials and connectivity. Verify password and access configuration.",
	},
	{
		Pattern:         `(?i)(relation .+ does not exist|column .+ does not exist|schema .+ does not exist|table .+ doesn't exist)`,
		Level:           AlertError,
		Category:        "database",
		Description:     "Database schema error",
		SuggestedAction: "Run database migrations. Check if the schema is up to date.",
	},
	{
		Pattern:         `(?i)(too many connections|remaining connection slots are reserved|max_connections)`,
		Level:           AlertCritical,
		Category:        "database",
		Description:     "Database connection limit reached",
		SuggestedAction: "Check for connection leaks. Increase max_connections or use connection pooling.",
	},
	// Auth errors (generic)
	{
		Pattern:         `(?i)(jwt|token).*(expired|invalid|malformed)`,
		Level:           AlertError,
		Category:        "auth",
		Description:     "Authentication token error",
		SuggestedAction: "Check auth secret configuration. Verify token expiry settings.",
	},
	// Network errors (generic)
	{
		Pattern:         `(?i)(connection refused|ECONNREFUSED)`,
		Level:           AlertError,
		Category:        "network",
		Description:     "Connection refused",
		SuggestedAction: "Check if the target service is running and accessible.",
	},
	// Resource errors
	{
		Pattern:         `(?i)(OOM|out of memory|Cannot allocate memory|oom-kill)`,
		Level:           AlertCritical,
		Category:        "resources",
		Description:     "Out of memory",
		SuggestedAction: "Increase memory limits or reduce workload. Check for memory leaks.",
	},
	{
		Pattern:         `(?i)(No space left on device|disk full|ENOSPC)`,
		Level:           AlertCritical,
		Category:        "resources",
		Description:     "Disk full",
		SuggestedAction: "Free disk space. Run docker system prune. Check for large log files.",
	},
	// Process signals
	{
		Pattern:         `(?i)(SIGTERM|SIGKILL|killed by signal)`,
		Level:           AlertWarning,
		Category:        "process",
		Description:     "Process terminated by signal",
		SuggestedAction: "Check if the service was intentionally stopped or hit resource limits.",
	},
	// Performance
	{
		Pattern:         `(?i)(timeout|timed out|deadline exceeded|context canceled)`,
		Level:           AlertWarning,
		Category:        "performance",
		Description:     "Operation timeout",
		SuggestedAction: "Check service load and response times. Consider increasing timeout values.",
	},
	{
		Pattern:         `(?i)(rate.?limit|too many requests|429)`,
		Level:           AlertWarning,
		Category:        "rate_limit",
		Description:     "Rate limiting triggered",
		SuggestedAction: "Review rate limit configuration. Check for misbehaving clients.",
	},
	// Permission errors
	{
		Pattern:         `(?i)(permission denied|access denied|forbidden|401 unauthorized)`,
		Level:           AlertError,
		Category:        "auth",
		Description:     "Permission or access denied",
		SuggestedAction: "Check file permissions, user roles, and service credentials.",
	},
}

var compiledPatterns []struct {
	re      *regexp.Regexp
	pattern ErrorPattern
}

func init() {
	compiledPatterns = make([]struct {
		re      *regexp.Regexp
		pattern ErrorPattern
	}, len(errorPatterns))
	for i, p := range errorPatterns {
		compiledPatterns[i].re = regexp.MustCompile(p.Pattern)
		compiledPatterns[i].pattern = p
	}
}

// AnalyzeResult from log analysis.
type AnalyzeResult struct {
	Service      string  `json:"service"`
	TotalLines   int     `json:"total_lines"`
	ErrorCount   int     `json:"error_count"`
	WarningCount int     `json:"warning_count"`
	Issues       []Issue `json:"issues"`
}

// Issue found during log analysis.
type Issue struct {
	Level       AlertLevel `json:"level"`
	Category    string     `json:"category"`
	Description string     `json:"description"`
	Action      string     `json:"suggested_action"`
	Count       int        `json:"count"`
	Example     string     `json:"example,omitempty"`
}

// AnalyzeLogs examines log entries for known error patterns.
func AnalyzeLogs(entries []LogEntry, service string) AnalyzeResult {
	result := AnalyzeResult{
		Service:    service,
		TotalLines: len(entries),
	}

	issueCounts := make(map[string]int)
	issueExamples := make(map[string]string)

	for _, entry := range entries {
		if entry.Level == "ERROR" || entry.Level == "FATAL" || entry.Level == "CRITICAL" {
			result.ErrorCount++
		}
		if entry.Level == "WARNING" || entry.Level == "WARN" {
			result.WarningCount++
		}

		for _, cp := range compiledPatterns {
			// Check service filter
			if cp.pattern.Services != nil {
				matched := false
				for _, s := range cp.pattern.Services {
					if s == service {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}

			if cp.re.MatchString(entry.Message) || cp.re.MatchString(entry.Raw) {
				key := cp.pattern.Category + ":" + cp.pattern.Description
				issueCounts[key]++
				if _, ok := issueExamples[key]; !ok {
					example := entry.Message
					if len(example) > 200 {
						example = example[:200] + "..."
					}
					issueExamples[key] = example
				}
			}
		}
	}

	// Build issue list
	for _, cp := range compiledPatterns {
		key := cp.pattern.Category + ":" + cp.pattern.Description
		count, ok := issueCounts[key]
		if !ok {
			continue
		}
		// Avoid duplicates
		delete(issueCounts, key)
		result.Issues = append(result.Issues, Issue{
			Level:       cp.pattern.Level,
			Category:    cp.pattern.Category,
			Description: cp.pattern.Description,
			Action:      cp.pattern.SuggestedAction,
			Count:       count,
			Example:     issueExamples[key],
		})
	}

	return result
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
