package engine

import (
	"context"
	"strconv"
	"strings"
)

// ResourceCollector gathers resource usage data.
type ResourceCollector struct {
	transport *Transport
}

// GetResourceUsage enriches service statuses with CPU/memory data.
func (c *ResourceCollector) GetResourceUsage(ctx context.Context, statuses []ServiceStatus) []ServiceStatus {
	res := c.transport.DockerCommand(ctx, "stats --no-stream --format '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}'")
	if !res.Success {
		return statuses
	}

	usage := make(map[string][2]float64) // name -> [cpu%, mem_mb]
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		cpuStr := strings.TrimSuffix(strings.TrimSpace(parts[1]), "%")
		cpu, _ := strconv.ParseFloat(cpuStr, 64)

		memStr := strings.TrimSpace(parts[2])
		memMB := parseMemoryMB(memStr)

		usage[name] = [2]float64{cpu, memMB}
	}

	for i := range statuses {
		// Try exact match first, then partial
		for name, u := range usage {
			if name == statuses[i].Name || strings.Contains(name, statuses[i].Name) {
				cpu := u[0]
				mem := u[1]
				statuses[i].CPUPercent = &cpu
				statuses[i].MemoryMB = &mem
				break
			}
		}
	}

	return statuses
}

// GetDiskUsage returns disk usage output.
func (c *ResourceCollector) GetDiskUsage(ctx context.Context) string {
	res := c.transport.ExecuteUnsafe(ctx, "df -h / /home 2>/dev/null | head -5")
	return res.Stdout
}

// GetSystemLoad returns system load averages.
func (c *ResourceCollector) GetSystemLoad(ctx context.Context) string {
	res := c.transport.ExecuteUnsafe(ctx, "cat /proc/loadavg 2>/dev/null || uptime")
	return strings.TrimSpace(res.Stdout)
}

// parseMemoryMB parses docker stats memory format like "123.4MiB / 1.5GiB"
func parseMemoryMB(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 0 {
		return 0
	}
	used := strings.TrimSpace(parts[0])
	used = strings.ToLower(used)

	var val float64
	if strings.HasSuffix(used, "gib") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(used, "gib"), 64)
		val = v * 1024
	} else if strings.HasSuffix(used, "mib") {
		val, _ = strconv.ParseFloat(strings.TrimSuffix(used, "mib"), 64)
	} else if strings.HasSuffix(used, "kib") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(used, "kib"), 64)
		val = v / 1024
	} else if strings.HasSuffix(used, "b") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(used, "b"), 64)
		val = v / (1024 * 1024)
	}
	return val
}
