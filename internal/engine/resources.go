package engine

import (
	"context"
	"strconv"
	"strings"
)

const (
	// dockerStatFields is the number of pipe-separated fields in docker stats output.
	dockerStatFields = 3
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
		parts := strings.SplitN(line, "|", dockerStatFields)
		if len(parts) < dockerStatFields {
			continue
		}
		name := strings.TrimSpace(parts[0])
		cpuStr := strings.TrimSuffix(strings.TrimSpace(parts[1]), "%")
		cpu, _ := strconv.ParseFloat(cpuStr, 64)

		memStr := strings.TrimSpace(parts[2])
		memMB := ParseDockerMemoryMB(memStr)

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
