package engine

import (
	"fmt"
	"regexp"
)

var errorPatterns = []ErrorPattern{
	{
		Pattern:         `(?i)(FATAL|authentication failed|could not connect to server|password authentication failed)`,
		Level:           AlertCritical,
		Category:        "database",
		Description:     "PostgreSQL authentication or startup failure",
		SuggestedAction: "Check PostgreSQL credentials and connectivity. Verify pg_hba.conf and POSTGRES_PASSWORD env var.",
		Services:        []string{"postgres"},
	},
	{
		Pattern:         `(?i)(relation .+ does not exist|column .+ does not exist|schema .+ does not exist)`,
		Level:           AlertError,
		Category:        "database",
		Description:     "Database schema error",
		SuggestedAction: "Run database migrations. Check if the schema is up to date.",
		Services:        []string{"postgres", "hasura"},
	},
	{
		Pattern:         `(?i)(too many connections|remaining connection slots are reserved)`,
		Level:           AlertCritical,
		Category:        "database",
		Description:     "PostgreSQL connection limit reached",
		SuggestedAction: "Check for connection leaks. Increase max_connections or use connection pooling.",
		Services:        []string{"postgres"},
	},
	{
		Pattern:         `(?i)(collation version mismatch)`,
		Level:           AlertWarning,
		Category:        "database",
		Description:     "Collation version mismatch",
		SuggestedAction: "Run ALTER COLLATION to update or reindex affected databases.",
		Services:        []string{"postgres"},
	},
	{
		Pattern:         `(?i)(metadata inconsistency|inconsistent object)`,
		Level:           AlertError,
		Category:        "graphql",
		Description:     "Hasura metadata inconsistency",
		SuggestedAction: "Reload Hasura metadata. Check if database schema matches Hasura expectations.",
		Services:        []string{"hasura"},
	},
	{
		Pattern:         `(?i)(jwt|token).*(expired|invalid|malformed)`,
		Level:           AlertError,
		Category:        "auth",
		Description:     "JWT token error",
		SuggestedAction: "Check JWT secret configuration. Verify token expiry settings.",
		Services:        []string{"hasura", "supabase-auth"},
	},
	{
		Pattern:         `(?i)(workflow.*failed|execution.*error)`,
		Level:           AlertWarning,
		Category:        "workflow",
		Description:     "n8n workflow execution failure",
		SuggestedAction: "Check n8n workflow logs for specific error details.",
		Services:        []string{"n8n"},
	},
	{
		Pattern:         `(?i)(connection refused|ECONNREFUSED)`,
		Level:           AlertError,
		Category:        "network",
		Description:     "Connection refused",
		SuggestedAction: "Check if the target service is running and accessible.",
		Services:        []string{"n8n"},
	},
	{
		Pattern:         `(?i)(credential.*not found|credentials.*missing)`,
		Level:           AlertError,
		Category:        "credentials",
		Description:     "Missing credentials",
		SuggestedAction: "Re-configure credentials in n8n settings.",
		Services:        []string{"n8n"},
	},
	{
		Pattern:         `(?i)(gotrue.*error|auth.*service.*error)`,
		Level:           AlertError,
		Category:        "auth",
		Description:     "Supabase GoTrue auth error",
		SuggestedAction: "Check GoTrue configuration and database connectivity.",
		Services:        []string{"supabase-auth"},
	},
	{
		Pattern:         `(?i)(oauth.*fail|oauth.*error|provider.*error)`,
		Level:           AlertError,
		Category:        "auth",
		Description:     "OAuth authentication failure",
		SuggestedAction: "Verify OAuth provider credentials and callback URLs.",
		Services:        []string{"supabase-auth"},
	},
	{
		Pattern:         `(?i)(cuda|gpu).*(error|fail|unavailable)`,
		Level:           AlertError,
		Category:        "gpu",
		Description:     "GPU/CUDA error",
		SuggestedAction: "Check GPU drivers and CUDA installation. Verify nvidia-docker runtime.",
		Services:        []string{"embedding-service"},
	},
	{
		Pattern:         `(?i)(model.*load.*fail|failed to load model|model not found)`,
		Level:           AlertCritical,
		Category:        "model",
		Description:     "ML model loading failure",
		SuggestedAction: "Check model file path and permissions. Verify model download.",
		Services:        []string{"embedding-service"},
	},
	{
		Pattern:         `(?i)(OOM|out of memory|Cannot allocate memory|oom-kill)`,
		Level:           AlertCritical,
		Category:        "resources",
		Description:     "Out of memory",
		SuggestedAction: "Increase container memory limits or reduce workload. Check for memory leaks.",
	},
	{
		Pattern:         `(?i)(No space left on device|disk full|ENOSPC)`,
		Level:           AlertCritical,
		Category:        "resources",
		Description:     "Disk full",
		SuggestedAction: "Free disk space. Run docker system prune. Check for large log files.",
	},
	{
		Pattern:         `(?i)(SIGTERM|SIGKILL|killed by signal)`,
		Level:           AlertWarning,
		Category:        "process",
		Description:     "Process terminated by signal",
		SuggestedAction: "Check if the service was intentionally stopped or hit resource limits.",
	},
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
