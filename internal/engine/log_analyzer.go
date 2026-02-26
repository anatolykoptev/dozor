package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// logLevelError is the string for ERROR level log entries.
	logLevelError = "ERROR"
	// logLevelFatal is the string for FATAL level log entries.
	logLevelFatal = "FATAL"
	// logLevelCritical is the string for CRITICAL level log entries.
	logLevelCritical = "CRITICAL"
	// timelineBuckets is the number of hourly buckets in the error timeline.
	timelineBuckets = 24
	// timelineBarWidth is the maximum bar width in the ASCII histogram.
	timelineBarWidth = 30
	// issueExampleMaxLen is the maximum length of an issue example message.
	issueExampleMaxLen = 200
	// normalizeMaxLen is the maximum length for a normalized error message template.
	normalizeMaxLen = 120
	// topClusterCount is the maximum number of error clusters returned.
	topClusterCount = 5
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

// compiledErrorPattern is a pre-compiled error pattern for efficient matching.
type compiledErrorPattern struct {
	re      *regexp.Regexp
	pattern ErrorPattern
}

var compiledPatterns []compiledErrorPattern

func init() {
	compiledPatterns = make([]compiledErrorPattern, len(errorPatterns))
	for i, p := range errorPatterns {
		compiledPatterns[i].re = regexp.MustCompile(p.Pattern)
		compiledPatterns[i].pattern = p
	}
}

// LabelPattern creates an ErrorPattern from a user-supplied regex string (dozor.logs.pattern label).
func LabelPattern(pattern string) ErrorPattern {
	return ErrorPattern{
		Pattern:         pattern,
		Level:           AlertWarning,
		Category:        "custom",
		Description:     "Custom pattern: " + pattern,
		SuggestedAction: "Review matches for custom log pattern.",
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

// buildEffectivePatterns merges built-in compiled patterns with user-supplied extras.
func buildEffectivePatterns(extraPatterns []ErrorPattern) []compiledErrorPattern {
	if len(extraPatterns) == 0 {
		return compiledPatterns
	}
	all := make([]compiledErrorPattern, len(compiledPatterns), len(compiledPatterns)+len(extraPatterns))
	copy(all, compiledPatterns)
	for _, ep := range extraPatterns {
		re, err := regexp.Compile(ep.Pattern)
		if err != nil {
			continue // skip invalid regex
		}
		all = append(all, compiledErrorPattern{re: re, pattern: ep})
	}
	return all
}

// patternMatchesEntry returns true if cp matches the entry and its service filter allows service.
func patternMatchesEntry(cp compiledErrorPattern, entry LogEntry, service string) bool {
	if cp.pattern.Services != nil {
		allowed := false
		for _, s := range cp.pattern.Services {
			if s == service {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return cp.re.MatchString(entry.Message) || cp.re.MatchString(entry.Raw)
}

// countLogLevels increments error/warning counters in result based on entry level.
func countLogLevels(result *AnalyzeResult, entry LogEntry) {
	if entry.IsErrorLevel() {
		result.ErrorCount++
	}
	if entry.Level == "WARNING" || entry.Level == "WARN" {
		result.WarningCount++
	}
}

// matchEntryToPatterns checks entry against all patterns and records counts/examples.
func matchEntryToPatterns(entry LogEntry, service string, allPatterns []compiledErrorPattern, issueCounts map[string]int, issueExamples map[string]string) {
	for _, cp := range allPatterns {
		if !patternMatchesEntry(cp, entry, service) {
			continue
		}
		key := cp.pattern.Category + ":" + cp.pattern.Description
		issueCounts[key]++
		if _, ok := issueExamples[key]; !ok {
			example := entry.Message
			if len(example) > issueExampleMaxLen {
				example = example[:issueExampleMaxLen] + "..."
			}
			issueExamples[key] = example
		}
	}
}

// AnalyzeLogs examines log entries for known error patterns.
// Extra patterns (e.g. from dozor.logs.pattern labels) are appended to built-in patterns.
func AnalyzeLogs(entries []LogEntry, service string, extraPatterns ...ErrorPattern) AnalyzeResult {
	result := AnalyzeResult{
		Service:    service,
		TotalLines: len(entries),
	}

	allPatterns := buildEffectivePatterns(extraPatterns)
	issueCounts := make(map[string]int)
	issueExamples := make(map[string]string)

	for _, entry := range entries {
		countLogLevels(&result, entry)
		matchEntryToPatterns(entry, service, allPatterns, issueCounts, issueExamples)
	}

	// Build issue list preserving pattern order, skipping duplicates.
	for _, cp := range allPatterns {
		key := cp.pattern.Category + ":" + cp.pattern.Description
		count, ok := issueCounts[key]
		if !ok {
			continue
		}
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

// Regexps for normalizing error messages — compiled once at package level.
var (
	normalizeTimestampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?Z?\s*`)
	normalizeIPRe        = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?\b`)
	normalizeUUIDRe      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	normalizeHexRe       = regexp.MustCompile(`\b0x[0-9a-fA-F]{4,}\b`)
	normalizeNumRe       = regexp.MustCompile(`\b\d{2,}\b`)
)

// normalizeErrorMessage replaces variable parts with placeholders for grouping.
func normalizeErrorMessage(msg string) string {
	s := normalizeTimestampRe.ReplaceAllString(msg, "")
	s = normalizeIPRe.ReplaceAllString(s, "<IP>")
	s = normalizeUUIDRe.ReplaceAllString(s, "<UUID>")
	s = normalizeHexRe.ReplaceAllString(s, "<HEX>")
	s = normalizeNumRe.ReplaceAllString(s, "<N>")
	s = strings.TrimSpace(s)
	if len(s) > normalizeMaxLen {
		s = s[:normalizeMaxLen]
	}
	return s
}

// AnalyzeErrorTimeline buckets errors per hour for the last 24h and returns an ASCII histogram.
func AnalyzeErrorTimeline(entries []LogEntry) string {
	now := time.Now()
	buckets := make([]int, timelineBuckets)

	for _, e := range entries {
		if e.Timestamp == nil {
			continue
		}
		if e.Level != logLevelError && e.Level != logLevelFatal && e.Level != logLevelCritical {
			continue
		}
		age := now.Sub(*e.Timestamp)
		if age < 0 || age > timelineBuckets*time.Hour {
			continue
		}
		hour := int(age.Hours())
		if hour >= timelineBuckets {
			hour = timelineBuckets - 1
		}
		buckets[timelineBuckets-1-hour]++
	}

	// Find max for scaling
	maxVal := 0
	total := 0
	for _, v := range buckets {
		if v > maxVal {
			maxVal = v
		}
		total += v
	}

	if total == 0 {
		return "No errors in last 24 hours.\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Error Timeline (last 24h, %d total)\n", total)
	for i, v := range buckets {
		hour := now.Add(-time.Duration(timelineBuckets-1-i) * time.Hour)
		fmt.Fprint(&b, timelineBarLine(hour, v, maxVal))
	}
	return b.String()
}

// timelineBarLine renders a single row in the error timeline ASCII histogram.
func timelineBarLine(hour time.Time, v, maxVal int) string {
	label := hour.Format("15:00")
	if v == 0 {
		return fmt.Sprintf("  %s |%*s\n", label, timelineBarWidth, "")
	}
	width := 1
	if maxVal > 0 {
		width = (v * timelineBarWidth) / maxVal
		if width == 0 {
			width = 1
		}
	}
	bar := strings.Repeat("█", width)
	return fmt.Sprintf("  %s |%-*s %d\n", label, timelineBarWidth, bar, v)
}

// ClusterErrors groups similar errors by normalized template.
func ClusterErrors(entries []LogEntry) []ErrorCluster {
	clusters := make(map[string]*ErrorCluster)

	for _, e := range entries {
		if e.Level != logLevelError && e.Level != logLevelFatal && e.Level != logLevelCritical {
			continue
		}
		template := normalizeErrorMessage(e.Message)
		if template == "" {
			template = normalizeErrorMessage(e.Raw)
		}
		if template == "" {
			continue
		}
		if c, ok := clusters[template]; ok {
			c.Count++
		} else {
			example := e.Message
			if len(example) > issueExampleMaxLen {
				example = example[:issueExampleMaxLen] + "..."
			}
			clusters[template] = &ErrorCluster{
				Template: template,
				Count:    1,
				Example:  example,
			}
		}
	}

	result := make([]ErrorCluster, 0, len(clusters))
	for _, c := range clusters {
		result = append(result, *c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	// Return top 5
	if len(result) > topClusterCount {
		result = result[:topClusterCount]
	}
	return result
}

// FormatErrorClusters formats error clusters for display.
func FormatErrorClusters(clusters []ErrorCluster) string {
	if len(clusters) == 0 {
		return "No error clusters found.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Top Error Clusters (%d)\n", len(clusters))
	for i, c := range clusters {
		fmt.Fprintf(&b, "  %d. [%dx] %s\n", i+1, c.Count, c.Template)
		fmt.Fprintf(&b, "     Example: %s\n", c.Example)
	}
	return b.String()
}
