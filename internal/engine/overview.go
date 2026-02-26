package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	// ioWaitWarnPct is the I/O wait percentage threshold for showing a warning.
	ioWaitWarnPct = 5
	// netFieldsMin is the minimum number of fields in a parsed network line.
	netFieldsMin = 3
	// dfFieldCount is the number of fields expected in a df output line.
	dfFieldCount = 6
	// diskCriticalPct is the disk usage percentage to trigger a critical alert.
	diskCriticalPct = 90
	// diskWarnPct is the disk usage percentage to trigger a warning.
	diskWarnPct = 80
	// inodeWarnPct is the inode usage percentage threshold for showing a warning.
	inodeWarnPct = 50
)

// GetOverview returns a system-level dashboard.
func (a *ServerAgent) GetOverview(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("System Overview\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	overviewWriteMemory(ctx, &b, a)
	overviewWriteDisk(ctx, &b, a)
	overviewWriteInodes(ctx, &b, a)
	overviewWriteLoad(ctx, &b, a)
	overviewWriteCPU(ctx, &b, a)
	overviewWriteIOWait(ctx, &b, a)
	overviewWriteUptime(ctx, &b, a)
	overviewWriteNetworkIO(ctx, &b, a)
	overviewWriteTopProcesses(ctx, &b, a)
	overviewWriteDocker(ctx, &b, a)
	overviewWriteSystemd(ctx, &b, a)

	return b.String()
}

func overviewWriteMemory(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "free -h 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		return
	}
	b.WriteString("Memory:\n")
	b.WriteString(res.Stdout)
	for _, line := range strings.Split(res.Stdout, "\n") {
		if !strings.HasPrefix(line, "Swap:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[2] != "0B" && fields[2] != "0" {
			fmt.Fprintf(b, "\nSwap in use: %s of %s total\n", fields[2], fields[1])
		}
	}
	b.WriteString("\n")
}

func overviewWriteDisk(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	disk := a.resources.GetDiskUsage(ctx)
	if disk == "" {
		return
	}
	b.WriteString("Disk:\n")
	b.WriteString(disk)
	b.WriteString("\n")
}

func overviewWriteInodes(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "df -i / /home 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		return
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] == "Filesystem" {
			continue
		}
		pctStr := strings.TrimSuffix(fields[4], "%")
		pct, err := strconv.Atoi(pctStr)
		if err == nil && pct > inodeWarnPct {
			fmt.Fprintf(b, "Inodes [!!]: %s at %d%% on %s\n", fields[0], pct, fields[len(fields)-1])
		}
	}
}

func overviewWriteLoad(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	load := a.resources.GetSystemLoad(ctx)
	if load != "" {
		fmt.Fprintf(b, "Load: %s\n\n", load)
	}
}

func overviewWriteCPU(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "nproc 2>/dev/null")
	if res.Success {
		fmt.Fprintf(b, "CPUs: %s\n", strings.TrimSpace(res.Stdout))
	}
}

func overviewWriteIOWait(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "awk '/^cpu /{total=0; for(i=2;i<=NF;i++) total+=$i; iowait=$6; printf \"%.1f\", (iowait/total)*100}' /proc/stat 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		return
	}
	iowait := strings.TrimSpace(res.Stdout)
	icon := "OK"
	var pct float64
	_, _ = fmt.Sscanf(iowait, "%f", &pct)
	if pct > ioWaitWarnPct {
		icon = "!!"
	}
	fmt.Fprintf(b, "I/O Wait [%s]: %s%%\n", icon, iowait)
}

func overviewWriteUptime(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "uptime -p 2>/dev/null || uptime")
	if res.Success {
		fmt.Fprintf(b, "Uptime: %s\n", strings.TrimSpace(res.Stdout))
	}
}

func overviewWriteNetworkIO(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, `awk 'NR>2{iface=$1; gsub(/:$/,"",iface); if(iface!="lo"){rx=$2; tx=$10; printf "%s RX=%d TX=%d\n", iface, rx, tx}}' /proc/net/dev 2>/dev/null`)
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("\nNetwork I/O:\n")
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		parts := strings.Fields(line)
		if len(parts) < netFieldsMin {
			continue
		}
		iface := parts[0]
		var rx, tx int64
		_, _ = fmt.Sscanf(parts[1], "RX=%d", &rx)
		_, _ = fmt.Sscanf(parts[2], "TX=%d", &tx)
		fmt.Fprintf(b, "  %s: RX %s / TX %s\n", iface, formatBytes(rx), formatBytes(tx))
	}
}

func overviewWriteTopProcesses(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "ps aux --sort=-%cpu 2>/dev/null | head -6")
	if res.Success && res.Stdout != "" {
		b.WriteString("\nTop processes (CPU):\n")
		b.WriteString(res.Stdout)
	}
}

func overviewWriteDocker(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	services := a.resolveServices(ctx, nil)
	if len(services) == 0 {
		return
	}
	statuses := a.status.GetAllStatuses(ctx, services)
	running, stopped := 0, 0
	for _, s := range statuses {
		if s.State == StateRunning {
			running++
		} else {
			stopped++
		}
	}
	fmt.Fprintf(b, "\nDocker: %d running, %d stopped (of %d total)\n", running, stopped, len(statuses))
}

func overviewWriteSystemd(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	systemdSvcs := a.cfg.SystemdServices
	if len(systemdSvcs) == 0 {
		for _, us := range a.ResolveUserServices(ctx) {
			systemdSvcs = append(systemdSvcs, us.Name)
		}
	}
	if len(systemdSvcs) == 0 {
		return
	}
	b.WriteString("\nSystemd services:\n")
	for _, svc := range systemdSvcs {
		state := a.systemctlIsActive(ctx, svc)
		icon := "OK"
		if state != stateActive {
			icon = "!!"
		}
		fmt.Fprintf(b, "  [%s] %s (%s)\n", icon, svc, state)
	}
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
		if len(fields) < dfFieldCount {
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
		pctStr := strings.TrimSuffix(fields[4], "%")
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			continue
		}
		availGB := ParseSizeGB(fields[3])

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
		if dp.UsedPct >= diskCriticalPct {
			status = displayIconCritical
		} else if dp.UsedPct >= diskWarnPct {
			status = displayIconWarning
		}
		fmt.Fprintf(b, "\nDisk: %s %.0f%% (%.0fG free) — %s\n", dp.Filesystem, dp.UsedPct, dp.AvailGB, status)
	}
}
