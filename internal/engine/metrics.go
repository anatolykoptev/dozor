package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	// iostatAwaitWarnMs is the await threshold in ms for disk warning.
	iostatAwaitWarnMs = 20
	// iostatUtilWarnPct is the %util threshold for disk warning.
	iostatUtilWarnPct = 70
	// sarFieldsMin is the minimum fields in a sar output line.
	sarFieldsMin = 8
	// iostatFieldsMin is the minimum fields in an iostat -x line.
	iostatFieldsMin = 7
	// metricsTrendWindow is the number of recent data points shown in trend output.
	metricsTrendWindow = 6
	// iostatDeviceFieldsMin is the minimum fields in an iostat device line.
	iostatDeviceFieldsMin = 5
)

// GetMetrics returns detailed system metrics using sar, iostat, and vnstat.
func (a *ServerAgent) GetMetrics(ctx context.Context, period, service string) string {
	var b strings.Builder
	b.WriteString("System Metrics\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	metricsWriteSarCPU(ctx, &b, a, period)
	metricsWriteSarMemory(ctx, &b, a, period)
	metricsWriteIOStat(ctx, &b, a)
	metricsWriteVnstat(ctx, &b, a, period)

	if service != "" {
		metricsWritePidstat(ctx, &b, a, service)
	}

	return b.String()
}

// metricsWriteSarCPU writes historical CPU usage from sar.
func metricsWriteSarCPU(ctx context.Context, b *strings.Builder, a *ServerAgent, period string) {
	cmd := sarCommand(period) + " -u"
	res := a.transport.ExecuteUnsafe(ctx, cmd+" 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		b.WriteString("CPU History: sar not available (install sysstat)\n\n")
		return
	}

	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	b.WriteString("CPU History (sar):\n")

	// Find summary line (Average or last lines)
	for _, line := range lines {
		if strings.HasPrefix(line, "Average:") || strings.HasPrefix(line, "Среднее:") {
			fields := strings.Fields(line)
			if len(fields) >= sarFieldsMin {
				user := fields[2]
				system := fields[4]
				iowait := fields[5]
				idle := fields[len(fields)-1]
				fmt.Fprintf(b, "  Average: user=%s%% sys=%s%% iowait=%s%% idle=%s%%\n", user, system, iowait, idle)
			}
			break
		}
	}

	// Show last 6 data points for trend
	dataLines := filterSarDataLines(lines)
	start := 0
	if len(dataLines) > metricsTrendWindow {
		start = len(dataLines) - metricsTrendWindow
	}
	if len(dataLines) > 0 {
		b.WriteString("  Recent:\n")
		for _, line := range dataLines[start:] {
			fields := strings.Fields(line)
			if len(fields) >= sarFieldsMin {
				fmt.Fprintf(b, "    %s  user=%s%% sys=%s%% iowait=%s%% idle=%s%%\n",
					fields[0], fields[2], fields[4], fields[5], fields[len(fields)-1])
			}
		}
	}
	b.WriteString("\n")
}

// metricsWriteSarMemory writes historical memory usage from sar.
// sar -r columns: kbmemfree kbavail kbmemused %memused kbbuffers kbcached ...
//
//nolint:gocognit // parser for variable sar output layouts; splitting further hurts readability
func metricsWriteSarMemory(ctx context.Context, b *strings.Builder, a *ServerAgent, period string) {
	cmd := sarCommand(period) + " -r"
	res := a.transport.ExecuteUnsafe(ctx, cmd+" 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		return
	}

	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	b.WriteString("Memory History (sar):\n")

	// Find header to locate %memused column index
	memUsedIdx := -1
	for _, line := range lines {
		if strings.Contains(line, "%memused") {
			headers := strings.Fields(line)
			for i, h := range headers {
				if h == "%memused" {
					memUsedIdx = i
					break
				}
			}
			break
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "Average:") || strings.HasPrefix(line, "Среднее:") {
			fields := strings.Fields(line)
			if memUsedIdx > 0 && memUsedIdx < len(fields) {
				fmt.Fprintf(b, "  Average: memused=%s%%\n", fields[memUsedIdx])
			} else if len(fields) >= iostatDeviceFieldsMin {
				// Fallback: try field 4 as %memused
				fmt.Fprintf(b, "  Average: memused=%s%%\n", fields[4])
			}
			break
		}
	}
	b.WriteString("\n")
}

// metricsWriteIOStat writes disk I/O details from iostat.
func metricsWriteIOStat(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "iostat -x -d 1 1 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		b.WriteString("Disk I/O: iostat not available (install sysstat)\n\n")
		return
	}

	b.WriteString("Disk I/O (iostat):\n")
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	inDevice := false
	for _, line := range lines {
		if strings.HasPrefix(line, "Device") {
			inDevice = true
			continue
		}
		if !inDevice || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < iostatFieldsMin {
			continue
		}
		device := fields[0]
		// Skip loop and dm devices
		if strings.HasPrefix(device, "loop") || strings.HasPrefix(device, "dm-") {
			continue
		}

		// iostat -x columns: Device r/s w/s ... await ... %util (last column)
		awaitStr := findIOStatField(lines, fields, "await")
		utilStr := fields[len(fields)-1]

		icon := "OK"
		if await := parseFloat(awaitStr); await > iostatAwaitWarnMs {
			icon = "!!"
		}
		if util := parseFloat(utilStr); util > iostatUtilWarnPct {
			icon = "!!"
		}

		fmt.Fprintf(b, "  [%s] %s: r/s=%s w/s=%s await=%sms util=%s%%\n",
			icon, device, fields[1], fields[2], awaitStr, utilStr)
	}
	b.WriteString("\n")
}

// metricsWriteVnstat writes network traffic stats from vnstat.
func metricsWriteVnstat(ctx context.Context, b *strings.Builder, a *ServerAgent, period string) {
	flag := vnstatFlag(period)
	res := a.transport.ExecuteUnsafe(ctx, "vnstat "+flag+" 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		b.WriteString("Traffic: vnstat not available or no data yet\n\n")
		return
	}

	b.WriteString("Network Traffic (vnstat):\n")
	b.WriteString(strings.TrimSpace(res.Stdout))
	b.WriteString("\n\n")
}

// metricsWritePidstat writes per-process stats for a given service.
func metricsWritePidstat(ctx context.Context, b *strings.Builder, a *ServerAgent, service string) {
	// Find PID(s) for the service via docker or systemctl
	pidCmd := fmt.Sprintf(
		`docker inspect --format '{{.State.Pid}}' %s 2>/dev/null || systemctl show --property=MainPID --value %s 2>/dev/null`,
		service, service)
	pidRes := a.transport.ExecuteUnsafe(ctx, pidCmd)
	pid := strings.TrimSpace(pidRes.Stdout)
	if pid == "" || pid == "0" {
		return
	}

	res := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("pidstat -p %s -u -r -d 1 1 2>/dev/null", pid))
	if !res.Success || res.Stdout == "" {
		return
	}

	fmt.Fprintf(b, "Process Stats for %s (PID %s):\n", service, pid)
	b.WriteString(strings.TrimSpace(res.Stdout))
	b.WriteString("\n\n")
}

// sarCommand returns the sar command with appropriate flags for the period.
func sarCommand(period string) string {
	switch period {
	case "yesterday":
		return "sar -f /var/log/sa/sa$(date -d yesterday +%d)"
	case "week":
		// sar keeps daily files; show 7 days of averages via sadf
		return "for i in $(seq 0 6); do d=$(date -d \"$i days ago\" +%d); [ -f /var/log/sa/sa$d ] && sar -f /var/log/sa/sa$d; done | grep -E '^Average|^Среднее'"
	default:
		return "sar"
	}
}

// vnstatFlag returns the vnstat flag for the given period.
func vnstatFlag(period string) string {
	switch period {
	case "yesterday":
		return "-d 2"
	case "week":
		return "-w"
	default:
		return "-d 1"
	}
}

// findIOStatField finds a field value by header name in iostat output.
func findIOStatField(allLines []string, dataFields []string, header string) string {
	for _, line := range allLines {
		if !strings.HasPrefix(line, "Device") {
			continue
		}
		headers := strings.Fields(line)
		for i, h := range headers {
			if h == header && i < len(dataFields) {
				return dataFields[i]
			}
		}
	}
	// Fallback: await is typically field index 6 in extended mode
	if header == "await" && len(dataFields) > 6 {
		return dataFields[6]
	}
	return "?"
}

// filterSarDataLines returns only data lines from sar output (skip headers/blanks).
func filterSarDataLines(lines []string) []string {
	var data []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip headers and summary
		if strings.HasPrefix(line, "Linux") || strings.HasPrefix(line, "Average") ||
			strings.HasPrefix(line, "Среднее") || strings.Contains(line, "%user") ||
			strings.Contains(line, "%idle") && !strings.Contains(line, ".") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= sarFieldsMin {
			// First field should be a timestamp (HH:MM:SS)
			if strings.Contains(fields[0], ":") {
				data = append(data, line)
			}
		}
	}
	return data
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}
