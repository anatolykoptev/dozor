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

	if len(problematic) > 0 {
		fmt.Fprintf(&b, "\nServices needing attention (%d):\n", len(problematic))
		for _, s := range problematic {
			tag := "WARNING"
			level := s.GetAlertLevel()
			if level == AlertCritical {
				tag = "CRITICAL"
			} else if level == AlertError {
				tag = "ERROR"
			}

			fmt.Fprintf(&b, "\n[%s] %s", tag, s.Name)
			parts := []string{string(s.State)}
			if s.RestartCount > 0 {
				parts = append(parts, fmt.Sprintf("%d restarts", s.RestartCount))
			}
			if s.ErrorCount > 0 {
				parts = append(parts, fmt.Sprintf("%d errors", s.ErrorCount))
			}
			fmt.Fprintf(&b, " — %s\n", strings.Join(parts, ", "))

			// Run log analysis for this service
			if s.State == StateRunning && s.ErrorCount > 0 {
				entries := a.logs.GetLogs(ctx, s.Name, a.cfg.LogLines, false)
				analysis := AnalyzeLogs(entries, s.Name)
				for _, issue := range analysis.Issues {
					fmt.Fprintf(&b, "  Issue: %s (%d occurrences)\n", issue.Description, issue.Count)
					fmt.Fprintf(&b, "  Action: %s\n", issue.Action)
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
					fmt.Fprintf(&b, "    [%s] %s\n", ts, msg)
				}
			}
		}
	}

	if len(healthy) > 0 {
		fmt.Fprintf(&b, "\nHealthy services (%d): %s\n", len(healthy), strings.Join(healthy, ", "))
	}

	a.appendDiskPressure(ctx, &b)

	if len(overridden) > 0 {
		fmt.Fprintf(&b, "\nP0 OVERRIDE — dev-excluded but DOWN: %s\n", strings.Join(overridden, ", "))
	}
	if len(excluded) > 0 {
		fmt.Fprintf(&b, "\nDev-excluded (%d): %s\n", len(excluded), strings.Join(excluded, ", "))
	}

	return b.String()
}
