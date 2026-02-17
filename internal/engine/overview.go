package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

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
		fs := fields[0]
		if fs == "Filesystem" {
			continue
		}
		mount := fields[len(fields)-1]
		if strings.HasPrefix(fs, "tmpfs") || strings.HasPrefix(fs, "devtmpfs") ||
			strings.HasPrefix(fs, "overlay") || strings.HasPrefix(fs, "shm") ||
			strings.HasPrefix(fs, "udev") || strings.HasPrefix(fs, "none") {
			continue
		}
		pctStr := fields[4]
		pctStr = strings.TrimSuffix(pctStr, "%")
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			continue
		}
		availStr := fields[3]
		availGB := ParseSizeGB(availStr)

		pressures = append(pressures, DiskPressure{
			Filesystem: fs,
			UsedPct:    pct,
			AvailGB:    availGB,
			MountPoint: mount,
		})
	}
	return pressures
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
