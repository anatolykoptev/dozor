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
	cleanup   *CleanupCollector
	updates   *UpdatesCollector
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
		cleanup:   &CleanupCollector{transport: t},
		updates:   &UpdatesCollector{transport: t, cfg: cfg},
	}
}

// GetConfig returns the agent configuration.
func (a *ServerAgent) GetConfig() Config {
	return a.cfg
}

// resolveServices returns the services list: explicit > config > auto-discover.
func (a *ServerAgent) resolveServices(ctx context.Context, services []string) []string {
	if len(services) > 0 {
		return services
	}
	if len(a.cfg.Services) > 0 {
		return a.cfg.Services
	}
	return a.status.DiscoverServices(ctx)
}

// Diagnose runs full diagnostics on the server.
func (a *ServerAgent) Diagnose(ctx context.Context, services []string) DiagnosticReport {
	services = a.resolveServices(ctx, services)

	if len(services) == 0 {
		return DiagnosticReport{
			Timestamp:     time.Now(),
			Server:        a.cfg.Host,
			OverallHealth: "healthy",
		}
	}

	statuses := a.status.GetAllStatuses(ctx, services)
	statuses = a.resources.GetResourceUsage(ctx, statuses)

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

// CheckUpdates scans binaries for available updates.
func (a *ServerAgent) CheckUpdates(ctx context.Context) []TrackedBinary {
	return a.updates.CheckUpdates(ctx)
}

// InstallUpdate downloads and installs the latest release for a binary.
func (a *ServerAgent) InstallUpdate(ctx context.Context, binary string) (string, error) {
	return a.updates.InstallUpdate(ctx, binary)
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
	services := a.resolveServices(ctx, nil)
	if len(services) == 0 {
		return "No Docker services found. Use mode=overview for system info, or mode=systemd for systemd services."
	}
	statuses := a.status.GetAllStatuses(ctx, services)
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

// GetRemoteStatus returns remote server monitoring results.
func (a *ServerAgent) GetRemoteStatus(ctx context.Context) string {
	status := CheckRemoteServer(ctx, a.cfg)
	if status == nil {
		return "No remote server configured. Set DOZOR_REMOTE_HOST and/or DOZOR_REMOTE_URL in .env."
	}
	return FormatRemoteStatus(status)
}

// RemoteExec executes a validated command on the remote server via SSH.
func (a *ServerAgent) RemoteExec(ctx context.Context, command string) CommandResult {
	if a.cfg.RemoteHost == "" {
		return CommandResult{
			Stderr:  "no remote server configured (set DOZOR_REMOTE_HOST)",
			Success: false,
		}
	}
	t := newRemoteTransport(a.cfg)
	return t.Execute(ctx, command)
}

// ProbeURLs checks HTTP endpoints and returns results.
func (a *ServerAgent) ProbeURLs(ctx context.Context, urls []string, timeoutSec int) []ProbeResult {
	return ProbeURLs(ctx, urls, timeoutSec)
}

// ScanCerts finds and parses TLS certificates on the server.
func (a *ServerAgent) ScanCerts(ctx context.Context) []CertInfo {
	return ScanCerts(ctx)
}

// ScanPorts returns all listening ports.
func (a *ServerAgent) ScanPorts(ctx context.Context) []PortInfo {
	return ScanPorts(ctx, a.transport)
}

// GetGitStatusAt returns git deployment status for a repository path.
// Falls back to the directory containing the compose file, then ".".
func (a *ServerAgent) GetGitStatusAt(ctx context.Context, path string) GitStatus {
	if path == "" && a.cfg.ComposePath != "" {
		// Use the directory containing docker-compose.yml
		idx := strings.LastIndex(a.cfg.ComposePath, "/")
		if idx > 0 {
			path = a.cfg.ComposePath[:idx]
		} else {
			path = "."
		}
	}
	if path == "" {
		path = "."
	}
	return a.GetGitStatus(ctx, path)
}
