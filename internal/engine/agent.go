package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ServerAgent orchestrates all collectors for server diagnostics.
type ServerAgent struct {
	cfg       Config
	transport *Transport
	status    *StatusCollector
	resources *ResourceCollector
	logs      *LogCollector
	security  *SecurityCollector
	alerts    *AlertGenerator
}

// NewAgent creates a new server agent with all collectors.
func NewAgent(cfg Config) *ServerAgent {
	t := NewTransport(cfg)
	return &ServerAgent{
		cfg:       cfg,
		transport: t,
		status:    &StatusCollector{transport: t},
		resources: &ResourceCollector{transport: t},
		logs:      &LogCollector{transport: t},
		security:  &SecurityCollector{transport: t, cfg: cfg},
		alerts:    &AlertGenerator{cfg: cfg},
	}
}

// Diagnose runs full diagnostics on the server.
func (a *ServerAgent) Diagnose(ctx context.Context, services []string) DiagnosticReport {
	if len(services) == 0 {
		services = a.cfg.Services
	}

	// Get container statuses
	statuses := a.status.GetAllStatuses(ctx, services)

	// Enrich with resource usage
	statuses = a.resources.GetResourceUsage(ctx, statuses)

	// Get error counts from logs
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

	// Generate alerts
	alerts := a.alerts.GenerateAlerts(statuses)

	report := DiagnosticReport{
		Timestamp: time.Now(),
		Server:    a.cfg.Host,
		Services:  statuses,
		Alerts:    alerts,
	}
	report.CalculateHealth()

	return report
}

// GetServiceStatus returns status for a single service.
func (a *ServerAgent) GetServiceStatus(ctx context.Context, service string) ServiceStatus {
	s := a.status.GetContainerStatus(ctx, service)
	// Enrich with resources
	enriched := a.resources.GetResourceUsage(ctx, []ServiceStatus{s})
	if len(enriched) > 0 {
		s = enriched[0]
	}
	return s
}

// GetLogs returns parsed logs for a service.
func (a *ServerAgent) GetLogs(ctx context.Context, service string, lines int, errorsOnly bool) []LogEntry {
	return a.logs.GetLogs(ctx, service, lines, errorsOnly)
}

// AnalyzeLogs runs error pattern analysis on service logs.
func (a *ServerAgent) AnalyzeLogs(ctx context.Context, service string) AnalyzeResult {
	entries := a.logs.GetLogs(ctx, service, a.cfg.LogLines, false)
	return AnalyzeLogs(entries, service)
}

// RestartService restarts a docker compose service.
func (a *ServerAgent) RestartService(ctx context.Context, service string) CommandResult {
	return a.transport.DockerComposeCommand(ctx, "restart "+service)
}

// ExecuteCommand runs a validated command.
func (a *ServerAgent) ExecuteCommand(ctx context.Context, command string) CommandResult {
	return a.transport.Execute(ctx, command)
}

// CheckSecurity runs all security checks.
func (a *ServerAgent) CheckSecurity(ctx context.Context) []SecurityIssue {
	return a.security.CheckAll(ctx)
}

// GetHealth returns a quick health summary.
func (a *ServerAgent) GetHealth(ctx context.Context) string {
	statuses := a.status.GetAllStatuses(ctx, a.cfg.Services)
	var b strings.Builder
	allOK := true
	for _, s := range statuses {
		icon := "OK"
		if s.State != StateRunning {
			icon = "!!"
			allOK = false
		}
		fmt.Fprintf(&b, "[%s] %s (%s)\n", icon, s.Name, s.State)
	}
	if allOK {
		b.WriteString("\nAll services healthy.")
	} else {
		b.WriteString("\nSome services need attention.")
	}
	return b.String()
}

// PruneDocker cleans up docker resources.
func (a *ServerAgent) PruneDocker(ctx context.Context, images, buildCache, volumes bool, age string) string {
	var results []string

	if images {
		cmd := "image prune -af"
		if age != "" {
			cmd += " --filter until=" + age
		}
		res := a.transport.DockerCommand(ctx, cmd)
		results = append(results, "Images: "+res.Output())
	}

	if buildCache {
		cmd := "builder prune -af"
		if age != "" {
			cmd += " --filter until=" + age
		}
		res := a.transport.DockerCommand(ctx, cmd)
		results = append(results, "Build cache: "+res.Output())
	}

	if volumes {
		res := a.transport.DockerCommand(ctx, "volume prune -f")
		results = append(results, "Volumes: "+res.Output())
	}

	// Show disk usage after
	diskRes := a.transport.DockerCommand(ctx, "system df")
	results = append(results, "\nDisk usage:\n"+diskRes.Output())

	return strings.Join(results, "\n")
}

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
