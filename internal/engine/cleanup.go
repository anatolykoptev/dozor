package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CleanupCollector auto-detects and cleans system resources.
type CleanupCollector struct {
	transport *Transport
}

// allTargets is the list of all supported cleanup target names.
var allTargets = []string{"docker", "go", "npm", "uv", "pip", "journal", "tmp", "caches", "memory"}

// resolveTargets expands "all" and validates target names.
func resolveTargets(targets []string) []string {
	if len(targets) == 0 {
		return allTargets
	}
	for _, t := range targets {
		if t == "all" {
			return allTargets
		}
	}
	return targets
}

// ValidateCleanupTargets checks that all target names are valid.
func ValidateCleanupTargets(targets []string) (bool, string) {
	valid := make(map[string]bool, len(allTargets))
	for _, t := range allTargets {
		valid[t] = true
	}
	valid["all"] = true
	for _, t := range targets {
		if !valid[t] {
			return false, fmt.Sprintf("unknown target %q, valid: %s, all", t, strings.Join(allTargets, ", "))
		}
	}
	return true, ""
}

// Scan probes and measures reclaimable space for each target.
func (c *CleanupCollector) Scan(ctx context.Context, targets []string) []CleanupTarget {
	targets = resolveTargets(targets)
	results := make([]CleanupTarget, 0, len(targets))
	for _, name := range targets {
		results = append(results, c.scanTarget(ctx, name))
	}
	return results
}

// Clean executes cleanup for each target and returns results.
func (c *CleanupCollector) Clean(ctx context.Context, targets []string, minAge string) []CleanupTarget {
	targets = resolveTargets(targets)
	results := make([]CleanupTarget, 0, len(targets))
	for _, name := range targets {
		results = append(results, c.cleanTarget(ctx, name, minAge))
	}
	return results
}

func (c *CleanupCollector) probe(ctx context.Context, tool string) bool {
	res := c.transport.ExecuteUnsafe(ctx, "which "+tool+" 2>/dev/null")
	return res.Success && strings.TrimSpace(res.Stdout) != ""
}

func (c *CleanupCollector) scanTarget(ctx context.Context, name string) CleanupTarget {
	switch name {
	case "docker":
		return c.scanDocker(ctx)
	case "go":
		return c.scanGo(ctx)
	case "npm":
		return c.scanNpm(ctx)
	case "uv":
		return c.scanUv(ctx)
	case "pip":
		return c.scanPip(ctx)
	case "journal":
		return c.scanJournal(ctx)
	case "tmp":
		return c.scanTmp(ctx)
	case "caches":
		return c.scanCaches(ctx)
	case "memory":
		return c.scanMemory(ctx)
	default:
		return CleanupTarget{Name: name, Error: "unknown target"}
	}
}

func (c *CleanupCollector) cleanTarget(ctx context.Context, name, minAge string) CleanupTarget {
	switch name {
	case "docker":
		return c.cleanDocker(ctx, minAge)
	case "go":
		return c.cleanGo(ctx)
	case "npm":
		return c.cleanNpm(ctx)
	case "uv":
		return c.cleanUv(ctx)
	case "pip":
		return c.cleanPip(ctx)
	case "journal":
		return c.cleanJournal(ctx, minAge)
	case "tmp":
		return c.cleanTmp(ctx, minAge)
	case "caches":
		return c.cleanCaches(ctx)
	case "memory":
		return c.cleanMemory(ctx)
	default:
		return CleanupTarget{Name: name, Error: "unknown target"}
	}
}

// --- Parsing helpers ---

// parseDuMB parses "123\t/path" from du -sm output.
func parseDuMB(output string) float64 {
	output = strings.TrimSpace(output)
	if output == "" {
		return 0
	}
	lines := strings.SplitN(output, "\n", 2)
	parts := strings.Fields(lines[0])
	if len(parts) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(parts[0], 64)
	return v
}

// parseJournalSize parses journalctl --disk-usage output like
// "Archived and active journals take up 1.2G in the file system."
func parseJournalSize(output string) float64 {
	parts := strings.Fields(output)
	for i, p := range parts {
		if p == "up" && i+1 < len(parts) {
			return ParseSizeMB(parts[i+1])
		}
	}
	return 0
}

// parsePipCacheSize parses pip cache info output for size.
func parsePipCacheSize(output string) float64 {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Cache size:") {
			sizeStr := strings.TrimPrefix(line, "Cache size:")
			return ParseSizeMB(strings.TrimSpace(sizeStr))
		}
	}
	return 0
}

// extractDockerFreed parses docker prune output for "Total reclaimed space: X.XGB".
func extractDockerFreed(output string) float64 {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "reclaimed space") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return ParseSizeMB(strings.TrimSpace(parts[1]))
			}
		}
	}
	return 0
}

// daysFromDuration converts "7d", "24h" etc to number of days as string.
func daysFromDuration(d string) string {
	if d == "" {
		return "7"
	}
	d = strings.TrimSpace(d)
	numStr := d[:len(d)-1]
	unit := d[len(d)-1:]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return "7"
	}
	switch unit {
	case "d":
		return strconv.Itoa(num)
	case "h":
		days := num / 24
		if days < 1 {
			days = 1
		}
		return strconv.Itoa(days)
	case "m":
		return "1"
	default:
		return "7"
	}
}
