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
		// Tightened from `(?i)(rate.?limit|too many requests|429)` which produced
		// false positives on bare `429` substrings inside nanosecond timestamps
		// like `073474290Z`. Now requires either an explicit "rate limit" phrase,
		// "too many requests", or `429` in a recognizable HTTP-status context.
		Pattern:         `(?i)(\brate.?limit(?:ed|ing|s)?\b|too many requests|HTTP/\d\.\d\s+429\b|"status"\s*:\s*429\b|\bstatus[:= ]429\b|\b429\s+too\s+many\b)`,
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

// suppressedPatterns matches benign log noise that should be excluded from triage.
// These are GLOBAL — they apply to every service. For per-service "this is expected
// behavior of THIS service, not noise everywhere", use noiseRules below.
var suppressedPatterns = []string{
	`canceling statement due to user request`,
	`connection to client lost`,
	`context canceled`,
	`graph .* does not exist`,
}

var compiledSuppressed []*regexp.Regexp

// noiseRule marks log lines that match the expected behavior of a specific service.
// Unlike suppressedPatterns (silent global drop), noise hits are surfaced in
// AnalyzeResult.NoiseHits with a human-readable reason so the consuming agent
// sees what was suppressed and why — instead of being told "no issues" while
// silently hiding signal.
type noiseRule struct {
	// services scopes the rule. Empty = global. Otherwise the service name must match exactly.
	services []string
	re       *regexp.Regexp
	reason   string
}

// noiseRules is the curated list of known false-positive patterns. Each entry
// here represents a real bug previously caused by treating expected behavior
// as an incident. Adding a new entry is a one-line edit.
//
// Editorial rule: a pattern only belongs here if (a) it has been observed
// firing as a false alarm AND (b) the service truly is operating normally
// when the pattern matches. When in doubt, do NOT add — false negatives are
// worse than false positives in a monitoring tool.
var noiseRules = []noiseRule{
	{
		// cliproxyapi pools many Gemini keys and rotates round-robin. When one
		// key hits its rate limit, the proxy returns 502 with ~0s latency and
		// the next request transparently uses the next key. Sporadic 502s are
		// expected; the symptom is "5xx > 50% sustained for >10min", which is
		// not what a single 502 line looks like.
		services: []string{"cliproxyapi"},
		re: regexp.MustCompile(
			`gin_logger\.go:\d+\]\s+50[02]\s*\|\s*[0-9.]+m?s\s*\|\s*[0-9.]+\s*\|\s*POST\s+"?/v1/(chat/completions|messages|models|completions)`,
		),
		reason: "cliproxyapi round-robin LLM key rotation: a sporadic 502/500 with sub-second latency means cliproxyapi rotated to the next key on a rate-limit hit. NOT an outage. Only treat as incident if 5xx exceeds 50% sustained over 10 minutes.",
	},
	{
		// Headless Chromium on ARM probes for GPU hardware that does not exist
		// and logs ERROR-level lines while falling back to software rendering.
		// These have been firing for months without affecting any cloakbrowser
		// functionality. They are part of every browser context init.
		services: []string{"cloakbrowser"},
		re: regexp.MustCompile(
			`(?:SharedImageManager::ProduceSkia|IPH_ExtensionsZeroStatePromo|gles2_cmd_decoder_passthrough|user_education_interface_impl|gpu/command_buffer/service/shared_image)`,
		),
		reason: "ARM headless Chromium hardware probing — benign initialization warnings that fire on every browser context init and have no effect on functionality. Only treat as incident if the cloakbrowser container is currently restarting (check via docker_ps restart count) or actual chrome OOM events appear in dmesg with timestamps inside the last 10 minutes.",
	},
	{
		// go-code re-indexes the symbol graph in the background and occasionally
		// catches embed-jina mid-restart or under load. The retry next cycle
		// almost always succeeds. A single failure is not actionable.
		services: []string{"go-code"},
		re: regexp.MustCompile(
			`background index failed.*embed.*Post\s+"?http://embed-jina:`,
		),
		reason: "go-code background re-index transient failure to embed-jina; auto-retried on the next indexing cycle. Only treat as incident if the same error fires more than 3 times in 10 minutes.",
	},
}

func init() {
	compiledPatterns = make([]compiledErrorPattern, len(errorPatterns))
	for i, p := range errorPatterns {
		compiledPatterns[i].re = regexp.MustCompile(p.Pattern)
		compiledPatterns[i].pattern = p
	}
	compiledSuppressed = make([]*regexp.Regexp, len(suppressedPatterns))
	for i, p := range suppressedPatterns {
		compiledSuppressed[i] = regexp.MustCompile(`(?i)` + p)
	}
}

// recordNoiseHit bumps the noise counters and captures an example for a given rule.
// Extracted from AnalyzeLogs to keep the main loop flat (nestif lint).
func recordNoiseHit(result *AnalyzeResult, entry LogEntry, rule *noiseRule, counts map[string]int, examples map[string]string) {
	result.NoiseCount++
	counts[rule.reason]++
	if _, ok := examples[rule.reason]; ok {
		return
	}
	example := entry.Message
	if example == "" {
		example = entry.Raw
	}
	if len(example) > issueExampleMaxLen {
		example = example[:issueExampleMaxLen] + "..."
	}
	examples[rule.reason] = example
}

// matchNoiseRule returns the noiseRule that matches entry for the given service,
// or nil if no rule applies. A nil result means the entry should go through normal
// pattern matching.
func matchNoiseRule(entry LogEntry, service string) *noiseRule {
	for i := range noiseRules {
		r := &noiseRules[i]
		if len(r.services) > 0 {
			allowed := false
			for _, s := range r.services {
				if s == service {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		if r.re.MatchString(entry.Message) || r.re.MatchString(entry.Raw) {
			return r
		}
	}
	return nil
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

// isSuppressed returns true if the entry matches a known benign log pattern.
func isSuppressed(entry LogEntry) bool {
	for _, re := range compiledSuppressed {
		if re.MatchString(entry.Message) || re.MatchString(entry.Raw) {
			return true
		}
	}
	return false
}

// AnalyzeResult from log analysis.
type AnalyzeResult struct {
	Service         string     `json:"service"`
	TotalLines      int        `json:"total_lines"`
	ErrorCount      int        `json:"error_count"`
	WarningCount    int        `json:"warning_count"`
	SuppressedCount int        `json:"suppressed_count"`
	NoiseCount      int        `json:"noise_count"`
	NoiseHits       []NoiseHit `json:"noise_hits,omitempty"`
	Issues          []Issue    `json:"issues"`
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

// NoiseHit is a log entry (or group of identical entries) that matched a known
// noiseRule. Surfaced in AnalyzeResult so the consuming agent sees what was
// suppressed and why, instead of being shown a misleading "no issues" result.
type NoiseHit struct {
	Reason  string `json:"reason"`
	Count   int    `json:"count"`
	Example string `json:"example,omitempty"`
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
//
// Pipeline per entry:
//  1. global suppression (silently dropped, counted in SuppressedCount)
//  2. per-service noise rule (counted in NoiseCount, surfaced in NoiseHits, NOT promoted to issue)
//  3. error/warning level counting + pattern matching
func AnalyzeLogs(entries []LogEntry, service string, extraPatterns ...ErrorPattern) AnalyzeResult {
	result := AnalyzeResult{
		Service:    service,
		TotalLines: len(entries),
	}

	allPatterns := buildEffectivePatterns(extraPatterns)
	issueCounts := make(map[string]int)
	issueExamples := make(map[string]string)
	noiseCounts := make(map[string]int)
	noiseExamples := make(map[string]string)

	for _, entry := range entries {
		if isSuppressed(entry) {
			result.SuppressedCount++
			continue
		}
		if rule := matchNoiseRule(entry, service); rule != nil {
			recordNoiseHit(&result, entry, rule, noiseCounts, noiseExamples)
			continue
		}
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

	// Build noise hits, sorted by count descending so the loudest false positives
	// appear first in the output.
	for reason, count := range noiseCounts {
		result.NoiseHits = append(result.NoiseHits, NoiseHit{
			Reason:  reason,
			Count:   count,
			Example: noiseExamples[reason],
		})
	}
	sort.Slice(result.NoiseHits, func(i, j int) bool {
		return result.NoiseHits[i].Count > result.NoiseHits[j].Count
	})

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
