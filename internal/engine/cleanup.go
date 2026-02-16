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
var allTargets = []string{"docker", "go", "npm", "uv", "pip", "journal", "tmp", "caches"}

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
	default:
		return CleanupTarget{Name: name, Error: "unknown target"}
	}
}

// --- Docker ---

func (c *CleanupCollector) scanDocker(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "docker"}
	res := c.transport.ExecuteUnsafe(ctx, "docker system df --format '{{.Reclaimable}}' 2>/dev/null")
	if !res.Success {
		return t
	}
	t.Available = true
	// Sum reclaimable sizes from docker system df output
	var totalMB float64
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		totalMB += parseSizeMB(strings.TrimSpace(line))
	}
	t.SizeMB = totalMB
	return t
}

func (c *CleanupCollector) cleanDocker(ctx context.Context, minAge string) CleanupTarget {
	t := CleanupTarget{Name: "docker", Available: true}
	age := "24h"
	if minAge != "" {
		age = minAge
	}
	// Prune images
	res := c.transport.DockerCommand(ctx, "image prune -af --filter until="+age)
	freed := extractDockerFreed(res.Output())
	// Prune build cache
	res = c.transport.DockerCommand(ctx, "builder prune -af --filter until="+age)
	freed += extractDockerFreed(res.Output())
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// --- Go ---

func (c *CleanupCollector) scanGo(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "go"}
	if !c.probe(ctx, "go") {
		return t
	}
	t.Available = true
	// Get GOCACHE path and measure
	res := c.transport.ExecuteUnsafe(ctx, "go env GOCACHE 2>/dev/null")
	if res.Success {
		cachePath := strings.TrimSpace(res.Stdout)
		if cachePath != "" {
			t.SizeMB = c.duSizeMB(ctx, cachePath)
		}
	}
	// Also check GOMODCACHE
	res = c.transport.ExecuteUnsafe(ctx, "go env GOMODCACHE 2>/dev/null")
	if res.Success {
		modPath := strings.TrimSpace(res.Stdout)
		if modPath != "" {
			t.SizeMB += c.duSizeMB(ctx, modPath)
		}
	}
	return t
}

func (c *CleanupCollector) cleanGo(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "go"}
	if !c.probe(ctx, "go") {
		return t
	}
	t.Available = true
	before := t.SizeMB
	scan := c.scanGo(ctx)
	before = scan.SizeMB

	res := c.transport.ExecuteUnsafe(ctx, "go clean -cache 2>/dev/null")
	if !res.Success {
		t.Error = res.Stderr
		return t
	}
	after := c.scanGo(ctx)
	t.Freed = fmt.Sprintf("%.1f MB", before-after.SizeMB)
	return t
}

// --- npm ---

func (c *CleanupCollector) scanNpm(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "npm"}
	if !c.probe(ctx, "npm") {
		return t
	}
	t.Available = true
	res := c.transport.ExecuteUnsafe(ctx, "npm config get cache 2>/dev/null")
	if res.Success {
		cachePath := strings.TrimSpace(res.Stdout)
		if cachePath != "" {
			t.SizeMB = c.duSizeMB(ctx, cachePath)
		}
	}
	return t
}

func (c *CleanupCollector) cleanNpm(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "npm"}
	if !c.probe(ctx, "npm") {
		return t
	}
	t.Available = true
	before := c.scanNpm(ctx).SizeMB
	res := c.transport.ExecuteUnsafe(ctx, "npm cache clean --force 2>/dev/null")
	if !res.Success {
		t.Error = res.Stderr
		return t
	}
	after := c.scanNpm(ctx).SizeMB
	t.Freed = fmt.Sprintf("%.1f MB", before-after)
	return t
}

// --- uv ---

func (c *CleanupCollector) scanUv(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "uv"}
	if !c.probe(ctx, "uv") {
		return t
	}
	t.Available = true
	t.SizeMB = c.duSizeMB(ctx, "~/.cache/uv")
	return t
}

func (c *CleanupCollector) cleanUv(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "uv"}
	if !c.probe(ctx, "uv") {
		return t
	}
	t.Available = true
	before := c.scanUv(ctx).SizeMB
	res := c.transport.ExecuteUnsafe(ctx, "uv cache clean 2>/dev/null")
	if !res.Success {
		t.Error = res.Stderr
		return t
	}
	after := c.scanUv(ctx).SizeMB
	t.Freed = fmt.Sprintf("%.1f MB", before-after)
	return t
}

// --- pip ---

func (c *CleanupCollector) scanPip(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "pip"}
	if !c.probe(ctx, "pip") && !c.probe(ctx, "pip3") {
		return t
	}
	t.Available = true
	// Try pip cache info to get size
	res := c.transport.ExecuteUnsafe(ctx, "pip cache info 2>/dev/null || pip3 cache info 2>/dev/null")
	if res.Success {
		t.SizeMB = parsePipCacheSize(res.Stdout)
	}
	return t
}

func (c *CleanupCollector) cleanPip(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "pip"}
	if !c.probe(ctx, "pip") && !c.probe(ctx, "pip3") {
		return t
	}
	t.Available = true
	before := c.scanPip(ctx).SizeMB
	res := c.transport.ExecuteUnsafe(ctx, "pip cache purge 2>/dev/null || pip3 cache purge 2>/dev/null")
	if !res.Success {
		t.Error = res.Stderr
		return t
	}
	t.Freed = fmt.Sprintf("%.1f MB", before)
	return t
}

// --- journal ---

func (c *CleanupCollector) scanJournal(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "journal"}
	if !c.probe(ctx, "journalctl") {
		return t
	}
	t.Available = true
	res := c.transport.ExecuteUnsafe(ctx, "journalctl --disk-usage 2>/dev/null")
	if res.Success {
		t.SizeMB = parseJournalSize(res.Stdout)
	}
	return t
}

func (c *CleanupCollector) cleanJournal(ctx context.Context, minAge string) CleanupTarget {
	t := CleanupTarget{Name: "journal"}
	if !c.probe(ctx, "journalctl") {
		return t
	}
	t.Available = true
	vacuumTime := "3d"
	if minAge != "" {
		vacuumTime = minAge
	}
	before := c.scanJournal(ctx).SizeMB
	res := c.transport.ExecuteUnsafe(ctx, "journalctl --vacuum-time="+vacuumTime+" 2>/dev/null")
	if !res.Success {
		t.Error = res.Stderr
		return t
	}
	after := c.scanJournal(ctx).SizeMB
	t.Freed = fmt.Sprintf("%.1f MB", before-after)
	return t
}

// --- tmp ---

func (c *CleanupCollector) scanTmp(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "tmp", Available: true}
	res := c.transport.ExecuteUnsafe(ctx, "du -sm /tmp 2>/dev/null")
	if res.Success {
		t.SizeMB = parseDuMB(res.Stdout)
	}
	return t
}

func (c *CleanupCollector) cleanTmp(ctx context.Context, minAge string) CleanupTarget {
	t := CleanupTarget{Name: "tmp", Available: true}
	atime := "7"
	if minAge != "" {
		atime = daysFromDuration(minAge)
	}
	before := c.scanTmp(ctx).SizeMB
	cmd := fmt.Sprintf("find /tmp -type f -atime +%s -delete 2>/dev/null; echo done", atime)
	c.transport.ExecuteUnsafe(ctx, cmd)
	after := c.scanTmp(ctx).SizeMB
	t.Freed = fmt.Sprintf("%.1f MB", before-after)
	return t
}

// --- caches ---

func (c *CleanupCollector) scanCaches(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "caches", Available: true}
	t.SizeMB = c.duSizeMB(ctx, "~/.cache")
	return t
}

func (c *CleanupCollector) cleanCaches(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "caches", Available: true}
	// Remove known stale cache directories
	staleDirs := []string{
		"~/.cache/gopls",
		"~/.cache/node-gyp",
		"~/.cache/puppeteer",
		"~/.cache/pip",
		"~/.cache/yarn",
		"~/.cache/pnpm",
		"~/.cache/typescript",
	}
	var freedMB float64
	for _, dir := range staleDirs {
		sizeBefore := c.duSizeMB(ctx, dir)
		if sizeBefore > 0 {
			c.transport.ExecuteUnsafe(ctx, "rm -rf "+dir+" 2>/dev/null")
			freedMB += sizeBefore
		}
	}
	t.Freed = fmt.Sprintf("%.1f MB", freedMB)
	return t
}

// --- Helpers ---

// duSizeMB runs du -sm on a path and returns size in MB.
func (c *CleanupCollector) duSizeMB(ctx context.Context, path string) float64 {
	res := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("du -sm %s 2>/dev/null", path))
	if !res.Success {
		return 0
	}
	return parseDuMB(res.Stdout)
}

// parseDuMB parses "123\t/path" from du -sm output.
func parseDuMB(output string) float64 {
	output = strings.TrimSpace(output)
	if output == "" {
		return 0
	}
	// du -sm outputs: "SIZE_MB\tPATH" per line, take first line
	lines := strings.SplitN(output, "\n", 2)
	parts := strings.Fields(lines[0])
	if len(parts) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(parts[0], 64)
	return v
}

// parseSizeMB parses size strings like "1.5G", "500M", "100K", "(1.5 GB)".
func parseSizeMB(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "()")
	s = strings.ToUpper(s)

	// Try patterns like "1.5GB", "500MB", "100KB", "1.5G", "500M"
	var numStr string
	var unit string
	for i, c := range s {
		if (c >= '0' && c <= '9') || c == '.' {
			numStr += string(c)
		} else {
			unit = strings.TrimSpace(s[i:])
			break
		}
	}
	if numStr == "" {
		return 0
	}
	val, _ := strconv.ParseFloat(numStr, 64)

	switch {
	case strings.HasPrefix(unit, "T"):
		return val * 1024 * 1024
	case strings.HasPrefix(unit, "G"):
		return val * 1024
	case strings.HasPrefix(unit, "M"):
		return val
	case strings.HasPrefix(unit, "K"):
		return val / 1024
	case strings.HasPrefix(unit, "B"):
		return val / (1024 * 1024)
	default:
		return val
	}
}

// parseJournalSize parses journalctl --disk-usage output like
// "Archived and active journals take up 1.2G in the file system."
func parseJournalSize(output string) float64 {
	// Extract size from "take up X.XG" or "take up X.XM"
	parts := strings.Fields(output)
	for i, p := range parts {
		if p == "up" && i+1 < len(parts) {
			return parseSizeMB(parts[i+1])
		}
	}
	return 0
}

// parsePipCacheSize parses pip cache info output for size.
func parsePipCacheSize(output string) float64 {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Cache size:") || strings.HasPrefix(line, "Location:") {
			// "Cache size: 1.2 GB" or "Cache size: 500 MB"
			if strings.HasPrefix(line, "Cache size:") {
				sizeStr := strings.TrimPrefix(line, "Cache size:")
				return parseSizeMB(strings.TrimSpace(sizeStr))
			}
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
				return parseSizeMB(strings.TrimSpace(parts[1]))
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
