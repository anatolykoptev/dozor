package engine

import (
	"context"
	"fmt"
	"strconv"
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
	// Auto-discover from docker compose
	return a.status.DiscoverServices(ctx)
}

// Diagnose runs full diagnostics on the server.
func (a *ServerAgent) Diagnose(ctx context.Context, services []string) DiagnosticReport {
	services = a.resolveServices(ctx, services)

	if len(services) == 0 {
		// No Docker services — return empty report with server info
		return DiagnosticReport{
			Timestamp:     time.Now(),
			Server:        a.cfg.Host,
			OverallHealth: "healthy",
		}
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

// Triage performs full auto-diagnosis: discovers services, checks health,
// analyzes errors for problematic services, and includes disk pressure.
func (a *ServerAgent) Triage(ctx context.Context, services []string) string {
	services = a.resolveServices(ctx, services)

	var b strings.Builder
	now := time.Now()

	if len(services) == 0 {
		fmt.Fprintf(&b, "Server Triage Report\nHealth: unknown | Time: %s\n\n", now.Format("2006-01-02 15:04"))
		b.WriteString("No Docker services found.\n")
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
			// Severity tag
			tag := "WARNING"
			level := s.GetAlertLevel()
			if level == AlertCritical {
				tag = "CRITICAL"
			} else if level == AlertError {
				tag = "ERROR"
			}

			// Status line
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

	return b.String()
}

// appendDiskPressure adds disk info to a triage report.
func (a *ServerAgent) appendDiskPressure(ctx context.Context, b *strings.Builder) {
	pressures := a.GetDiskPressure(ctx)
	for _, dp := range pressures {
		status := "OK"
		if dp.UsedPct >= 90 {
			status = "CRITICAL"
		} else if dp.UsedPct >= 80 {
			status = "WARNING"
		}
		fmt.Fprintf(b, "\nDisk: %s %.0f%% (%.0fG free) — %s\n", dp.Filesystem, dp.UsedPct, dp.AvailGB, status)
	}
}

// AnalyzeAll runs error pattern analysis on all services, returning only those with issues.
func (a *ServerAgent) AnalyzeAll(ctx context.Context) string {
	services := a.resolveServices(ctx, nil)
	if len(services) == 0 {
		return "No services found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Log Analysis — All Services\n%s\n\n", strings.Repeat("=", 40))

	var clean []string
	found := false
	for _, svc := range services {
		result := a.AnalyzeLogs(ctx, svc)
		if len(result.Issues) == 0 && result.ErrorCount == 0 {
			clean = append(clean, svc)
			continue
		}
		found = true
		fmt.Fprintf(&b, "%s: %d errors, %d warnings\n", result.Service, result.ErrorCount, result.WarningCount)
		for _, issue := range result.Issues {
			fmt.Fprintf(&b, "  [%s] %s (%d occurrences)\n", issue.Level, issue.Description, issue.Count)
			fmt.Fprintf(&b, "    Action: %s\n", issue.Action)
		}
		b.WriteString("\n")
	}

	if !found {
		b.WriteString("No error patterns detected in any service.\n")
	}
	if len(clean) > 0 {
		fmt.Fprintf(&b, "Clean services: %s\n", strings.Join(clean, ", "))
	}

	return b.String()
}

// GetAllErrors collects ERROR/FATAL log lines from all services.
func (a *ServerAgent) GetAllErrors(ctx context.Context) string {
	services := a.resolveServices(ctx, nil)
	if len(services) == 0 {
		return "No services found."
	}

	var b strings.Builder
	b.WriteString("Errors across all services (last 100 lines each):\n\n")

	var clean []string
	found := false
	for _, svc := range services {
		errors := a.logs.GetErrorLogs(ctx, svc, 100)
		if len(errors) == 0 {
			clean = append(clean, svc)
			continue
		}
		found = true

		// Cap at 20 lines per service
		shown := errors
		if len(shown) > 20 {
			shown = shown[len(shown)-20:]
		}

		fmt.Fprintf(&b, "%s (%d errors):\n", svc, len(errors))
		for _, e := range shown {
			ts := ""
			if e.Timestamp != nil {
				ts = e.Timestamp.Format("15:04:05")
			}
			msg := e.Message
			if len(msg) > 200 {
				msg = msg[:200] + "..."
			}
			fmt.Fprintf(&b, "  [%s] %s\n", ts, msg)
		}
		if len(errors) > 20 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(errors)-20)
		}
		b.WriteString("\n")
	}

	if !found {
		b.WriteString("No errors found in any service.\n")
	}
	if len(clean) > 0 {
		fmt.Fprintf(&b, "Clean services: %s\n", strings.Join(clean, ", "))
	}

	return b.String()
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

// GetOverview returns a system-level dashboard.
func (a *ServerAgent) GetOverview(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("System Overview\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	// Memory info
	res := a.transport.ExecuteUnsafe(ctx, "free -h 2>/dev/null")
	if res.Success && res.Stdout != "" {
		b.WriteString("Memory:\n")
		b.WriteString(res.Stdout)
		b.WriteString("\n")
	}

	// Disk usage
	disk := a.resources.GetDiskUsage(ctx)
	if disk != "" {
		b.WriteString("Disk:\n")
		b.WriteString(disk)
		b.WriteString("\n")
	}

	// Load average
	load := a.resources.GetSystemLoad(ctx)
	if load != "" {
		fmt.Fprintf(&b, "Load: %s\n\n", load)
	}

	// CPU info
	res = a.transport.ExecuteUnsafe(ctx, "nproc 2>/dev/null")
	if res.Success {
		fmt.Fprintf(&b, "CPUs: %s\n", strings.TrimSpace(res.Stdout))
	}

	// Uptime
	res = a.transport.ExecuteUnsafe(ctx, "uptime -p 2>/dev/null || uptime")
	if res.Success {
		fmt.Fprintf(&b, "Uptime: %s\n", strings.TrimSpace(res.Stdout))
	}

	// Top processes by CPU
	res = a.transport.ExecuteUnsafe(ctx, "ps aux --sort=-%cpu 2>/dev/null | head -6")
	if res.Success && res.Stdout != "" {
		b.WriteString("\nTop processes (CPU):\n")
		b.WriteString(res.Stdout)
	}

	// Docker summary (if available)
	services := a.resolveServices(ctx, nil)
	if len(services) > 0 {
		statuses := a.status.GetAllStatuses(ctx, services)
		running, stopped := 0, 0
		for _, s := range statuses {
			if s.State == StateRunning {
				running++
			} else {
				stopped++
			}
		}
		fmt.Fprintf(&b, "\nDocker: %d running, %d stopped (of %d total)\n", running, stopped, len(statuses))
	}

	// Systemd services (if configured)
	if len(a.cfg.SystemdServices) > 0 {
		b.WriteString("\nSystemd services:\n")
		for _, svc := range a.cfg.SystemdServices {
			state := a.systemctlIsActive(ctx, svc)
			icon := "OK"
			if state != "active" {
				icon = "!!"
			}
			fmt.Fprintf(&b, "  [%s] %s (%s)\n", icon, svc, state)
		}
	}

	return b.String()
}

// GetSystemdStatus returns status of local systemd services.
func (a *ServerAgent) GetSystemdStatus(ctx context.Context, services []string) string {
	if len(services) == 0 {
		services = a.cfg.SystemdServices
	}
	if len(services) == 0 {
		return "No systemd services configured. Set DOZOR_SYSTEMD_SERVICES in .env."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Systemd Services (%d)\n\n", len(services))

	for _, svc := range services {
		state := a.systemctlIsActive(ctx, svc)
		icon := "OK"
		if state != "active" {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", icon, svc, state)

		// Get memory and uptime from systemctl show
		output := a.systemctlShow(ctx, svc, "ActiveEnterTimestamp,MemoryCurrent")
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ActiveEnterTimestamp=") {
				ts := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
				if ts != "" {
					fmt.Fprintf(&b, "  Started: %s\n", ts)
				}
			}
			if strings.HasPrefix(line, "MemoryCurrent=") {
				mem := strings.TrimPrefix(line, "MemoryCurrent=")
				if mem != "" && mem != "[not set]" && mem != "18446744073709551615" {
					if mb, ok := bytesToMB(mem); ok {
						fmt.Fprintf(&b, "  Memory: %.1f MB\n", mb)
					}
				}
			}
		}
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

// systemctlIsActive checks service status, trying --user first then system.
func (a *ServerAgent) systemctlIsActive(ctx context.Context, svc string) string {
	// Try user service first
	res := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl --user is-active %s 2>/dev/null", svc))
	state := strings.TrimSpace(res.Stdout)
	if state == "active" || state == "activating" || state == "deactivating" {
		return state
	}
	// Fall back to system service
	res = a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl is-active %s 2>/dev/null", svc))
	state = strings.TrimSpace(res.Stdout)
	if state == "" {
		return "unknown"
	}
	return state
}

// systemctlShow gets properties from systemctl show, trying --user first.
func (a *ServerAgent) systemctlShow(ctx context.Context, svc, properties string) string {
	res := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl --user show %s --property=%s 2>/dev/null", svc, properties))
	if res.Success && res.Stdout != "" && !strings.Contains(res.Stdout, "No such file") {
		return res.Stdout
	}
	res = a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl show %s --property=%s 2>/dev/null", svc, properties))
	return res.Stdout
}

// bytesToMB converts a byte count string to megabytes.
func bytesToMB(s string) (float64, bool) {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		} else {
			return 0, false
		}
	}
	if n <= 0 {
		return 0, false
	}
	return float64(n) / (1024 * 1024), true
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

// CleanupSystem scans or cleans system targets.
func (a *ServerAgent) CleanupSystem(ctx context.Context, targets []string, report bool, minAge string) string {
	if report {
		results := a.cleanup.Scan(ctx, targets)
		return formatScanResults(results)
	}
	results := a.cleanup.Clean(ctx, targets, minAge)
	return formatCleanResults(results)
}

// GetDiskPressure parses df -h output into structured data.
func (a *ServerAgent) GetDiskPressure(ctx context.Context) []DiskPressure {
	res := a.transport.ExecuteUnsafe(ctx, "df -h 2>/dev/null")
	if !res.Success {
		return nil
	}
	var pressures []DiskPressure
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		// Skip header and pseudo-filesystems
		fs := fields[0]
		if fs == "Filesystem" {
			continue
		}
		mount := fields[len(fields)-1]
		// Skip pseudo-filesystems
		if strings.HasPrefix(fs, "tmpfs") || strings.HasPrefix(fs, "devtmpfs") ||
			strings.HasPrefix(fs, "overlay") || strings.HasPrefix(fs, "shm") ||
			strings.HasPrefix(fs, "udev") || strings.HasPrefix(fs, "none") {
			continue
		}
		// Parse used percentage (e.g., "82%")
		pctStr := fields[4]
		pctStr = strings.TrimSuffix(pctStr, "%")
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			continue
		}
		// Parse available (e.g., "15G")
		availStr := fields[3]
		availGB := parseAvailGB(availStr)

		pressures = append(pressures, DiskPressure{
			Filesystem: fs,
			UsedPct:    pct,
			AvailGB:    availGB,
			MountPoint: mount,
		})
	}
	return pressures
}

// parseAvailGB parses human-readable sizes like "15G", "500M", "1.5T" to GB.
func parseAvailGB(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.ToUpper(s)
	unit := s[len(s)-1:]
	numStr := s[:len(s)-1]
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	switch unit {
	case "T":
		return val * 1024
	case "G":
		return val
	case "M":
		return val / 1024
	case "K":
		return val / (1024 * 1024)
	default:
		return 0
	}
}

func formatScanResults(results []CleanupTarget) string {
	var b strings.Builder
	b.WriteString("System Cleanup Scan\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	var totalMB float64
	for _, r := range results {
		if !r.Available {
			fmt.Fprintf(&b, "  [--] %-10s not available\n", r.Name)
			continue
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "  [!!] %-10s error: %s\n", r.Name, r.Error)
			continue
		}
		if r.SizeMB >= 1024 {
			fmt.Fprintf(&b, "  [OK] %-10s %.1f GB\n", r.Name, r.SizeMB/1024)
		} else {
			fmt.Fprintf(&b, "  [OK] %-10s %.0f MB\n", r.Name, r.SizeMB)
		}
		totalMB += r.SizeMB
	}

	b.WriteString("\n")
	if totalMB >= 1024 {
		fmt.Fprintf(&b, "Total reclaimable: %.1f GB\n", totalMB/1024)
	} else {
		fmt.Fprintf(&b, "Total reclaimable: %.0f MB\n", totalMB)
	}
	b.WriteString("Run server_cleanup({report: false}) to execute cleanup.\n")
	return b.String()
}

func formatCleanResults(results []CleanupTarget) string {
	var b strings.Builder
	b.WriteString("System Cleanup Results\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	for _, r := range results {
		if !r.Available {
			fmt.Fprintf(&b, "  [--] %-10s not available\n", r.Name)
			continue
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "  [!!] %-10s error: %s\n", r.Name, r.Error)
			continue
		}
		freed := r.Freed
		if freed == "" {
			freed = "0 MB"
		}
		fmt.Fprintf(&b, "  [OK] %-10s freed %s\n", r.Name, freed)
	}
	return b.String()
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
