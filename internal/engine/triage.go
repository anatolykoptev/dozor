package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// triageProbeTimeout is the timeout in seconds for healthcheck probes during triage.
	triageProbeTimeout = 5
	// triageRecentErrorsMax is the max number of recent errors to attach to a service.
	triageRecentErrorsMax = 3
	// triageErrorMsgMaxLen is the maximum length of an error message shown in triage.
	triageErrorMsgMaxLen = 150
)

// triageExclusionResult holds the outcome of applying dev-mode exclusions.
type triageExclusionResult struct {
	services   []string
	excluded   []string
	overridden []string
}

// triageApplyExclusions filters services by dev-mode exclusions, preserving critical overrides.
func (a *ServerAgent) triageApplyExclusions(ctx context.Context, services []string) triageExclusionResult {
	exclusions := a.ListExclusions()
	if len(exclusions) == 0 {
		return triageExclusionResult{services: services}
	}

	var excludedNames []string
	for _, svc := range services {
		if _, ok := exclusions[svc]; ok {
			excludedNames = append(excludedNames, svc)
		}
	}

	criticalExcluded := make(map[string]bool)
	for _, s := range a.status.GetAllStatuses(ctx, excludedNames) {
		if s.State == StateExited || s.State == StateDead || s.State == StateRestarting {
			criticalExcluded[s.Name] = true
		}
	}

	var result triageExclusionResult
	for _, svc := range services {
		if _, ok := exclusions[svc]; !ok {
			result.services = append(result.services, svc)
		} else if criticalExcluded[svc] {
			result.services = append(result.services, svc)
			result.overridden = append(result.overridden, svc)
		} else {
			result.excluded = append(result.excluded, svc)
		}
	}
	return result
}

// triageRunHealthchecks runs HTTP probes for services with a configured healthcheck URL.
func triageRunHealthchecks(ctx context.Context, statuses []ServiceStatus) []ServiceStatus {
	for i, s := range statuses {
		if s.State != StateRunning || s.HealthcheckURL == "" {
			continue
		}
		results := ProbeURLs(ctx, []string{s.HealthcheckURL}, triageProbeTimeout, false)
		if len(results) == 0 {
			continue
		}
		ok := results[0].OK
		statuses[i].HealthcheckOK = &ok
		if !ok {
			msg := fmt.Sprintf("status %d", results[0].Status)
			if results[0].Error != "" {
				msg = results[0].Error
			}
			statuses[i].HealthcheckMsg = msg
		}
	}
	return statuses
}

// startupGracePeriod is the window after container start during which errors are
// considered startup noise (e.g. dependency not ready yet) and are filtered out.
const startupGracePeriod = 90 * time.Second

// errorStalenessWindow defines how old an error must be to be considered stale.
// If ALL errors are older than this and the service is currently running,
// they're likely a one-time occurrence that has self-resolved.
const errorStalenessWindow = 5 * time.Minute

// triageEnrichErrors attaches error counts and recent error lines to each running service.
// It filters out startup noise (errors during the first 90s after container start)
// and stale errors (all errors older than 5 min with no recent recurrence).
func (a *ServerAgent) triageEnrichErrors(ctx context.Context, statuses []ServiceStatus) []ServiceStatus {
	now := time.Now()
	for i, s := range statuses {
		if s.State != StateRunning {
			continue
		}
		errors := a.logs.GetErrorLogs(ctx, s.Name, a.cfg.LogLines)
		errors = filterStartupErrors(errors, s.StartedAt)
		errors = filterStaleErrors(errors, now)
		errors = filterNoiseErrors(errors, s.Name)
		statuses[i].ErrorCount = len(errors)
		if len(errors) > triageRecentErrorsMax {
			statuses[i].RecentErrors = errors[len(errors)-triageRecentErrorsMax:]
		} else {
			statuses[i].RecentErrors = errors
		}
	}
	return statuses
}

// filterStartupErrors removes errors that occurred within the startup grace period.
func filterStartupErrors(errors []LogEntry, startedAt time.Time) []LogEntry {
	if startedAt.IsZero() {
		return errors
	}
	cutoff := startedAt.Add(startupGracePeriod)
	filtered := make([]LogEntry, 0, len(errors))
	for _, e := range errors {
		if e.Timestamp != nil && e.Timestamp.Before(cutoff) {
			continue // startup noise
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// filterStaleErrors returns empty if ALL errors are older than the staleness window.
// This handles the case where errors occurred once (e.g. transient network blip)
// but the service has been healthy since then.
func filterStaleErrors(errors []LogEntry, now time.Time) []LogEntry {
	if len(errors) == 0 {
		return errors
	}
	cutoff := now.Add(-errorStalenessWindow)
	for _, e := range errors {
		if e.Timestamp == nil || e.Timestamp.After(cutoff) {
			return errors // at least one recent error — keep all
		}
	}
	return nil // all errors are stale — discard
}

// triageOverallHealth computes the overall health string from problematic services.
func triageOverallHealth(problematic []ServiceStatus) string {
	overall := healthHealthy
	for _, s := range problematic {
		if s.State != StateRunning {
			return string(AlertCritical)
		}
		level := s.GetAlertLevel()
		switch {
		case level == AlertCritical:
			return string(AlertCritical)
		case level == AlertError && overall != string(AlertCritical):
			overall = healthDegraded
		case level == AlertWarning && overall == healthHealthy:
			overall = string(AlertWarning)
		}
	}
	return overall
}

// triageClassifyStatuses categorizes statuses into problematic/healthy and builds inhibited-services set.
func triageClassifyStatuses(alertGen *AlertGenerator, statuses []ServiceStatus) (problematic []ServiceStatus, healthy []string, inhibitedServices map[string]bool) {
	allAlerts := alertGen.GenerateAlerts(statuses)
	_, inhibitedAlerts := Inhibit(allAlerts, statuses)
	inhibitedServices = make(map[string]bool)
	for _, ia := range inhibitedAlerts {
		inhibitedServices[ia.Service] = true
	}
	for _, s := range statuses {
		if !s.IsHealthy() {
			problematic = append(problematic, s)
		} else {
			healthy = append(healthy, s.Name)
		}
	}
	return problematic, healthy, inhibitedServices
}

// triageHasGroups returns true if any status has a dozor.group label.
func triageHasGroups(statuses []ServiceStatus) bool {
	for _, s := range statuses {
		if s.DozorLabel("group") != "" {
			return true
		}
	}
	return false
}

// triageWriteInhibited appends an inhibited-services line to the builder.
func triageWriteInhibited(b *strings.Builder, inhibitedServices map[string]bool) {
	if len(inhibitedServices) == 0 {
		return
	}
	var inhibNames []string
	for name := range inhibitedServices {
		inhibNames = append(inhibNames, name)
	}
	fmt.Fprintf(b, "\nInhibited by dependency (%d): %s (parent service down)\n", len(inhibNames), strings.Join(inhibNames, ", "))
}

// Triage performs full auto-diagnosis: discovers services, checks health,
// analyzes errors for problematic services, and includes disk pressure.
func (a *ServerAgent) Triage(ctx context.Context, services []string) string {
	services = a.resolveServices(ctx, services)

	excl := a.triageApplyExclusions(ctx, services)
	services = excl.services

	var b strings.Builder
	now := time.Now()

	if a.IsDevMode() {
		b.WriteString("=== DEV MODE ACTIVE — observation only ===\n\n")
	}

	if len(services) == 0 {
		fmt.Fprintf(&b, "Server Triage Report\nHealth: unknown | Time: %s\n\n", now.Format("2006-01-02 15:04"))
		if len(excl.excluded) > 0 {
			fmt.Fprintf(&b, "All services dev-excluded (%d): %s\n", len(excl.excluded), strings.Join(excl.excluded, ", "))
		} else {
			b.WriteString("No Docker services found.\n")
		}
		a.appendDiskPressure(ctx, &b)
		return b.String()
	}

	statuses := a.status.GetAllStatuses(ctx, services)
	statuses = a.resources.GetResourceUsage(ctx, statuses)

	for i, s := range statuses {
		statuses[i].HealthcheckURL = s.DozorLabel("healthcheck.url")
		statuses[i].AlertChannel = s.DozorLabel("alert.channel")
	}

	statuses = triageRunHealthchecks(ctx, statuses)
	statuses = a.triageEnrichErrors(ctx, statuses)

	problematic, healthy, inhibitedServices := triageClassifyStatuses(a.alerts, statuses)

	overallHealth := triageOverallHealth(problematic)
	fmt.Fprintf(&b, "Server Triage Report\nHealth: %s | Time: %s\n", overallHealth, now.Format("2006-01-02 15:04"))

	if len(problematic) > 0 {
		b.WriteString("Issues: ")
		b.WriteString(triageIssueSummary(problematic))
		b.WriteString("\n")
	}

	if triageHasGroups(statuses) {
		a.writeGroupedTriage(ctx, &b, statuses)
	} else {
		a.writeFlatTriage(ctx, &b, problematic, healthy)
	}

	a.appendDiskPressure(ctx, &b)
	triageWriteInhibited(&b, inhibitedServices)

	if len(excl.overridden) > 0 {
		fmt.Fprintf(&b, "\nP0 OVERRIDE — dev-excluded but DOWN: %s\n", strings.Join(excl.overridden, ", "))
	}
	if len(excl.excluded) > 0 {
		fmt.Fprintf(&b, "\nDev-excluded (%d): %s\n", len(excl.excluded), strings.Join(excl.excluded, ", "))
	}

	return b.String()
}

// TriageIssue represents a searchable issue summary extracted from a triage report.
type TriageIssue struct {
	Service     string
	Level       AlertLevel // parsed from the [LEVEL] marker of the canonical line
	Description string     // e.g. "redis: 3 restarts, connection refused"
}

// TriageMachineSep separates `service` from `description` in a triage line.
// Format: `[LEVEL] service<TriageMachineSep>description`. Em-dash (U+2014) with
// surrounding spaces. Anyone emitting a machine-readable triage line MUST use
// this constant; replacing the em-dash with ASCII `-` silently breaks ExtractIssues.
const TriageMachineSep = " — "

// issueLevelPrefixes maps each canonical machine-line prefix to the AlertLevel
// it encodes. It is the single source of truth shared by FormatIssueLine (the
// only writer) and ExtractIssues (the only reader). Order matters only for the
// human-readable list — lookup is exact-prefix, so there is no [WARNING] vs
// [WARNING_HIGH] ambiguity (the trailing space disambiguates).
var issueLevelPrefixes = map[string]AlertLevel{
	"[CRITICAL] ":     AlertCritical,
	"[ERROR] ":        AlertError,
	"[WARNING_HIGH] ": AlertWarningHigh,
	"[WARNING] ":      AlertWarning,
}

// FormatIssueLine renders the single canonical machine alert line that
// ExtractIssues parses: `[LEVEL] service — description\n`. This is the ONLY
// place the line shape is written; every producer (disk pressure, systemd
// checks, LLM and remote alerts) routes through it so a new producer cannot
// drift into a format ExtractIssues silently ignores. An AlertInfo level (no
// machine token) yields an empty string — info entries are not issues.
func FormatIssueLine(level AlertLevel, service, description string) string {
	token := level.MachineToken()
	if token == "" {
		return ""
	}
	return fmt.Sprintf("[%s] %s%s%s\n", token, service, TriageMachineSep, description)
}

// AlertIssueLine renders an Alert as a canonical issue line for the mechanical
// watch report, giving each alert a STABLE per-entity service name so the dedup
// hash (which keys on service) distinguishes distinct failures. LLM alerts carry
// Service="llm:<model|key>" set at construction (llm_check.go) — stable per
// probed entity, independent of the error kind, so the SAME entity failing with
// different HTTP codes dedups as one issue. Remote alerts carry a distinct
// Service (URL or unit name); they are namespaced "remote:<service>" to avoid
// colliding with a local container of the same name.
func AlertIssueLine(a Alert) string {
	service := alertReportService(a)
	desc := a.Title
	if a.Description != "" {
		desc += ": " + a.Description
	}
	return FormatIssueLine(a.Level, service, desc)
}

// Namespace prefixes for service names that denote a non-local entity — an LLM
// model/key probe or a remote host/unit. These are written by alertReportService
// and are the ONLY namespaces an issue service name can carry. They mark the
// service as NOT remediable by the local auto-remediation path (which can only
// act on local docker/systemd entities): see IsNamespacedService.
const (
	llmServicePrefix    = "llm:"
	remoteServicePrefix = "remote:"
)

// alertReportService derives the stable per-entity service name used for an
// Alert inside the watch report. See AlertIssueLine for the rationale.
func alertReportService(a Alert) string {
	switch {
	case strings.HasPrefix(a.Service, llmServicePrefix):
		// Already namespaced at construction (llm_check.go) — stable per
		// probed model/key, independent of the error kind.
		return a.Service
	case a.Service == "llm":
		// Legacy producer without a per-entity name; Title is the best
		// stable-ish identity available.
		return llmServicePrefix + a.Title
	default:
		return remoteServicePrefix + a.Service
	}
}

// IsNamespacedService reports whether a triage issue's service name carries a
// namespace prefix written by alertReportService (an LLM probe or a remote
// host/unit). Such services are NOT remediable: local auto-remediation can only
// restart local docker/systemd entities, never a remote URL or an LLM key. A
// namespaced issue — at ANY level, including critical — must be report-only.
// This is the explicit, named inverse of "is this a local docker/systemd
// service the restart arm may act on", co-located with the prefix constants so
// a new namespace cannot drift out of sync with the remediation guard.
func IsNamespacedService(service string) bool {
	return strings.HasPrefix(service, llmServicePrefix) ||
		strings.HasPrefix(service, remoteServicePrefix)
}

// ExtractIssues parses a triage report string into searchable issue summaries.
// It is the sole reader of the canonical line shape written by FormatIssueLine.
func ExtractIssues(report string) []TriageIssue {
	var issues []TriageIssue
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		for prefix, level := range issueLevelPrefixes {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			rest := line[len(prefix):]
			parts := strings.SplitN(rest, TriageMachineSep, 2)
			if len(parts) != 2 {
				continue
			}
			service := strings.TrimSpace(parts[0])
			issues = append(issues, TriageIssue{
				Service:     service,
				Level:       level,
				Description: service + ": " + strings.TrimSpace(parts[1]),
			})
			break
		}
	}
	return issues
}

// triageIssueSummary builds a compact one-line summary of all problematic services.
// Example: "postgres(WARNING, 5 errors), go-code(ERROR, 1 restart)"
func triageIssueSummary(problematic []ServiceStatus) string {
	parts := make([]string, 0, len(problematic))
	for _, s := range problematic {
		level := s.GetAlertLevel()
		var details []string
		if s.State != StateRunning {
			details = append(details, string(s.State))
		}
		if s.RecentRestarts > 0 {
			details = append(details, fmt.Sprintf("%d restarts/24h", s.RecentRestarts))
		}
		if s.ErrorCount > 0 {
			details = append(details, fmt.Sprintf("%d errors", s.ErrorCount))
		}
		if len(details) == 0 {
			details = append(details, "unhealthy")
		}
		parts = append(parts, fmt.Sprintf("%s(%s, %s)", s.Name, level, strings.Join(details, ", ")))
	}
	return strings.Join(parts, ", ")
}

// writeFlatTriage renders the original flat problematic/healthy output.
func (a *ServerAgent) writeFlatTriage(ctx context.Context, b *strings.Builder, problematic []ServiceStatus, healthy []string) {
	for _, s := range problematic {
		b.WriteString("\n")
		a.writeServiceDetail(ctx, b, s)
	}

	if len(healthy) > 0 {
		fmt.Fprintf(b, "\nHealthy services (%d): %s\n", len(healthy), strings.Join(healthy, ", "))
	}
}

// writeGroupedTriage renders triage output organized by service groups.
func (a *ServerAgent) writeGroupedTriage(ctx context.Context, b *strings.Builder, statuses []ServiceStatus) {
	groups := GroupServices(statuses)

	for _, g := range groups {
		name := g.Name
		if name == "" {
			name = "ungrouped"
		}
		fmt.Fprintf(b, "\n--- %s (%s) ---\n", name, g.Health)

		for _, s := range g.Services {
			if s.IsHealthy() {
				fmt.Fprintf(b, "[OK] %s\n", s.Name)
			} else {
				a.writeServiceDetail(ctx, b, s)
			}
		}
	}
}

// writeServiceDetail renders a single problematic service with tag, details, and analysis.
func (a *ServerAgent) writeServiceDetail(ctx context.Context, b *strings.Builder, s ServiceStatus) {
	level := s.GetAlertLevel()
	var tag string
	switch level {
	case AlertCritical:
		tag = displayIconCritical
	case AlertError:
		tag = logLevelError
	default:
		tag = displayIconWarning
	}

	fmt.Fprintf(b, "[%s] %s", tag, s.Name)
	parts := []string{string(s.State)}
	if s.RecentRestarts > 0 {
		parts = append(parts, fmt.Sprintf("%d restarts/24h", s.RecentRestarts))
	}
	if s.ErrorCount > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", s.ErrorCount))
	}
	fmt.Fprintf(b, " — %s\n", strings.Join(parts, ", "))

	if s.HealthcheckOK != nil && !*s.HealthcheckOK {
		fmt.Fprintf(b, "  Healthcheck FAILED: %s -> %s\n", s.HealthcheckURL, s.HealthcheckMsg)
	}
	if s.AlertChannel != "" {
		fmt.Fprintf(b, "  Alert channel: %s\n", s.AlertChannel)
	}

	a.writeServiceAnalysis(ctx, b, s)
	writeServiceRecentErrors(b, s.RecentErrors)
}

// writeServiceAnalysis runs log analysis for a running service with errors and writes issues.
func (a *ServerAgent) writeServiceAnalysis(ctx context.Context, b *strings.Builder, s ServiceStatus) {
	if s.State != StateRunning || s.ErrorCount == 0 {
		return
	}
	entries := a.logs.GetLogs(ctx, s.Name, a.cfg.LogLines, false)
	var extra []ErrorPattern
	if p := s.DozorLabel("logs.pattern"); p != "" {
		extra = append(extra, LabelPattern(p))
	}
	analysis := AnalyzeLogs(entries, s.Name, extra...)
	for _, issue := range analysis.Issues {
		fmt.Fprintf(b, "  Issue: %s (%d occurrences)\n", issue.Description, issue.Count)
		fmt.Fprintf(b, "  Action: %s\n", issue.Action)
	}
}

// writeServiceRecentErrors writes recent error lines for a service.
func writeServiceRecentErrors(b *strings.Builder, errors []LogEntry) {
	if len(errors) == 0 {
		return
	}
	b.WriteString("  Recent errors:\n")
	for _, e := range errors {
		ts := ""
		if e.Timestamp != nil {
			ts = e.Timestamp.Format("15:04:05")
		}
		msg := e.Message
		if len(msg) > triageErrorMsgMaxLen {
			msg = msg[:triageErrorMsgMaxLen] + "..."
		}
		fmt.Fprintf(b, "    [%s] %s\n", ts, msg)
	}
}
