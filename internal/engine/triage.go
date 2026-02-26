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
	triageRecentErrorsMax = 5
	// triageErrorMsgMaxLen is the maximum length of an error message shown in triage.
	triageErrorMsgMaxLen = 150
)

// triageExclusionResult holds the outcome of applying dev-mode exclusions.
type triageExclusionResult struct {
	services  []string
	excluded  []string
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
	Description string // e.g. "redis: 3 restarts, connection refused"
}

// ExtractIssues parses a triage report string into searchable issue summaries.
func ExtractIssues(report string) []TriageIssue {
	var issues []TriageIssue
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"[CRITICAL] ", "[ERROR] ", "[WARNING] "} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			rest := line[len(prefix):]
			parts := strings.SplitN(rest, " — ", 2)
			if len(parts) != 2 {
				continue
			}
			issues = append(issues, TriageIssue{
				Service:     strings.TrimSpace(parts[0]),
				Description: strings.TrimSpace(parts[0]) + ": " + strings.TrimSpace(parts[1]),
			})
		}
	}
	return issues
}

// writeFlatTriage renders the original flat problematic/healthy output.
func (a *ServerAgent) writeFlatTriage(ctx context.Context, b *strings.Builder, problematic []ServiceStatus, healthy []string) {
	if len(problematic) > 0 {
		fmt.Fprintf(b, "\nServices needing attention (%d):\n", len(problematic))
		for _, s := range problematic {
			b.WriteString("\n")
			a.writeServiceDetail(ctx, b, s)
		}
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
	if s.RestartCount > 0 {
		parts = append(parts, fmt.Sprintf("%d restarts", s.RestartCount))
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
