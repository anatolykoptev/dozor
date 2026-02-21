package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Triage performs full auto-diagnosis: discovers services, checks health,
// analyzes errors for problematic services, and includes disk pressure.
func (a *ServerAgent) Triage(ctx context.Context, services []string) string {
	services = a.resolveServices(ctx, services)

	// Filter out dev-excluded services, but override for critical (exited/dead/restarting)
	var excluded []string
	var overridden []string
	if exclusions := a.ListExclusions(); len(exclusions) > 0 {
		// Pre-fetch statuses for excluded services to check for P0 override
		var excludedNames []string
		for _, svc := range services {
			if _, ok := exclusions[svc]; ok {
				excludedNames = append(excludedNames, svc)
			}
		}
		criticalExcluded := make(map[string]bool)
		if len(excludedNames) > 0 {
			for _, s := range a.status.GetAllStatuses(ctx, excludedNames) {
				if s.State == StateExited || s.State == StateDead || s.State == StateRestarting {
					criticalExcluded[s.Name] = true
				}
			}
		}

		filtered := services[:0:0]
		for _, svc := range services {
			if _, ok := exclusions[svc]; !ok {
				filtered = append(filtered, svc)
			} else if criticalExcluded[svc] {
				// P0 override: service is excluded but down — re-include it
				filtered = append(filtered, svc)
				overridden = append(overridden, svc)
			} else {
				excluded = append(excluded, svc)
			}
		}
		services = filtered
	}

	var b strings.Builder
	now := time.Now()

	// Dev mode banner
	if a.IsDevMode() {
		b.WriteString("=== DEV MODE ACTIVE — observation only ===\n\n")
	}

	if len(services) == 0 {
		fmt.Fprintf(&b, "Server Triage Report\nHealth: unknown | Time: %s\n\n", now.Format("2006-01-02 15:04"))
		if len(excluded) > 0 {
			fmt.Fprintf(&b, "All services dev-excluded (%d): %s\n", len(excluded), strings.Join(excluded, ", "))
		} else {
			b.WriteString("No Docker services found.\n")
		}
		a.appendDiskPressure(ctx, &b)
		return b.String()
	}

	// Get statuses with resource usage
	statuses := a.status.GetAllStatuses(ctx, services)
	statuses = a.resources.GetResourceUsage(ctx, statuses)

	// Extract dozor labels
	for i, s := range statuses {
		statuses[i].HealthcheckURL = s.DozorLabel("healthcheck.url")
		statuses[i].AlertChannel = s.DozorLabel("alert.channel")
	}

	// Run healthcheck probes for services with a configured URL
	for i, s := range statuses {
		if s.State != StateRunning || s.HealthcheckURL == "" {
			continue
		}
		results := ProbeURLs(ctx, []string{s.HealthcheckURL}, 5, false)
		if len(results) > 0 {
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
	}

	// Enrich with error counts
	for i, s := range statuses {
		if s.State == StateRunning {
			errors := a.logs.GetErrorLogs(ctx, s.Name, a.cfg.LogLines)
			statuses[i].ErrorCount = len(errors)
			if len(errors) > 5 {
				statuses[i].RecentErrors = errors[len(errors)-5:]
			} else {
				statuses[i].RecentErrors = errors
			}
		}
	}

	// Inhibit child alerts when a parent dependency is down.
	allAlerts := a.alerts.GenerateAlerts(statuses)
	_, inhibitedAlerts := Inhibit(allAlerts, statuses)
	inhibitedServices := make(map[string]bool)
	for _, ia := range inhibitedAlerts {
		inhibitedServices[ia.Service] = true
	}

	// Split into problematic vs healthy
	var problematic []ServiceStatus
	var healthy []string
	for _, s := range statuses {
		if !s.IsHealthy() {
			problematic = append(problematic, s)
		} else {
			healthy = append(healthy, s.Name)
		}
	}

	// Determine overall health
	overallHealth := "healthy"
	for _, s := range problematic {
		if s.State != StateRunning {
			overallHealth = "critical"
			break
		}
		level := s.GetAlertLevel()
		if level == AlertCritical {
			overallHealth = "critical"
			break
		}
		if level == AlertError && overallHealth != "critical" {
			overallHealth = "degraded"
		}
		if level == AlertWarning && overallHealth == "healthy" {
			overallHealth = "warning"
		}
	}

	fmt.Fprintf(&b, "Server Triage Report\nHealth: %s | Time: %s\n", overallHealth, now.Format("2006-01-02 15:04"))

	// Check if any service has a group label
	hasGroups := false
	for _, s := range statuses {
		if s.DozorLabel("group") != "" {
			hasGroups = true
			break
		}
	}

	if hasGroups {
		a.writeGroupedTriage(ctx, &b, statuses)
	} else {
		a.writeFlatTriage(ctx, &b, problematic, healthy)
	}

	a.appendDiskPressure(ctx, &b)

	if len(inhibitedServices) > 0 {
		var inhibNames []string
		for name := range inhibitedServices {
			inhibNames = append(inhibNames, name)
		}
		fmt.Fprintf(&b, "\nInhibited by dependency (%d): %s (parent service down)\n", len(inhibNames), strings.Join(inhibNames, ", "))
	}

	if len(overridden) > 0 {
		fmt.Fprintf(&b, "\nP0 OVERRIDE — dev-excluded but DOWN: %s\n", strings.Join(overridden, ", "))
	}
	if len(excluded) > 0 {
		fmt.Fprintf(&b, "\nDev-excluded (%d): %s\n", len(excluded), strings.Join(excluded, ", "))
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
		// Match lines like "[CRITICAL] redis — exited, 3 restarts"
		// or "[WARNING] api-service — running, 12 errors"
		// or "[ERROR] postgres — running, 5 errors"
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
	tag := "WARNING"
	level := s.GetAlertLevel()
	if level == AlertCritical {
		tag = "CRITICAL"
	} else if level == AlertError {
		tag = "ERROR"
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

	// Run log analysis for this service
	if s.State == StateRunning && s.ErrorCount > 0 {
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

	// Recent error lines (max 5)
	if len(s.RecentErrors) > 0 {
		b.WriteString("  Recent errors:\n")
		for _, e := range s.RecentErrors {
			ts := ""
			if e.Timestamp != nil {
				ts = e.Timestamp.Format("15:04:05")
			}
			msg := e.Message
			if len(msg) > 150 {
				msg = msg[:150] + "..."
			}
			fmt.Fprintf(b, "    [%s] %s\n", ts, msg)
		}
	}
}
