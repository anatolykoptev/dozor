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
		// Extract swap from free output
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.HasPrefix(line, "Swap:") {
				fields := strings.Fields(line)
				if len(fields) >= 3 && fields[2] != "0B" && fields[2] != "0" {
					fmt.Fprintf(&b, "\nSwap in use: %s of %s total\n", fields[2], fields[1])
				}
			}
		}
		b.WriteString("\n")
	}

	// Disk usage
	disk := a.resources.GetDiskUsage(ctx)
	if disk != "" {
		b.WriteString("Disk:\n")
		b.WriteString(disk)
		b.WriteString("\n")
	}

	// Inode usage — only report if > 50%
	res = a.transport.ExecuteUnsafe(ctx, "df -i / /home 2>/dev/null")
	if res.Success && res.Stdout != "" {
		for _, line := range strings.Split(res.Stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[0] == "Filesystem" {
				continue
			}
			pctStr := strings.TrimSuffix(fields[4], "%")
			pct, err := strconv.Atoi(pctStr)
			if err == nil && pct > 50 {
				fmt.Fprintf(&b, "Inodes [!!]: %s at %d%% on %s\n", fields[0], pct, fields[len(fields)-1])
			}
		}
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

	// I/O wait from /proc/stat
	res = a.transport.ExecuteUnsafe(ctx, "awk '/^cpu /{total=0; for(i=2;i<=NF;i++) total+=$i; iowait=$6; printf \"%.1f\", (iowait/total)*100}' /proc/stat 2>/dev/null")
	if res.Success && res.Stdout != "" {
		iowait := strings.TrimSpace(res.Stdout)
		icon := "OK"
		var pct float64
		fmt.Sscanf(iowait, "%f", &pct)
		if pct > 5 {
			icon = "!!"
		}
		fmt.Fprintf(&b, "I/O Wait [%s]: %s%%\n", icon, iowait)
	}

	// Uptime
	res = a.transport.ExecuteUnsafe(ctx, "uptime -p 2>/dev/null || uptime")
	if res.Success {
		fmt.Fprintf(&b, "Uptime: %s\n", strings.TrimSpace(res.Stdout))
	}

	// Network I/O from /proc/net/dev
	res = a.transport.ExecuteUnsafe(ctx, `awk 'NR>2{iface=$1; gsub(/:$/,"",iface); if(iface!="lo"){rx=$2; tx=$10; printf "%s RX=%d TX=%d\n", iface, rx, tx}}' /proc/net/dev 2>/dev/null`)
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("\nNetwork I/O:\n")
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}
			iface := parts[0]
			var rx, tx int64
			fmt.Sscanf(parts[1], "RX=%d", &rx)
			fmt.Sscanf(parts[2], "TX=%d", &tx)
			fmt.Fprintf(&b, "  %s: RX %s / TX %s\n", iface, formatBytes(rx), formatBytes(tx))
		}
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

func formatBytes(b int64) string {
	const (
		gb = 1024 * 1024 * 1024
		mb = 1024 * 1024
		kb = 1024
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
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
