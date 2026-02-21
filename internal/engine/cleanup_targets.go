package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// --- Docker ---

func (c *CleanupCollector) scanDocker(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "docker"}
	res := c.transport.ExecuteUnsafe(ctx, "docker system df --format '{{.Reclaimable}}' 2>/dev/null")
	if !res.Success {
		return t
	}
	t.Available = true
	var totalMB float64
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		totalMB += ParseSizeMB(strings.TrimSpace(line))
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
	var freed float64
	var details []string

	// Remove stopped containers
	res := c.transport.DockerCommand(ctx, "container prune -f --filter until="+age)
	if f := extractDockerFreed(res.Output()); f > 0 {
		freed += f
		details = append(details, fmt.Sprintf("containers: %.1f MB", f))
	}

	// Remove dangling/old images
	res = c.transport.DockerCommand(ctx, "image prune -af --filter until="+age)
	if f := extractDockerFreed(res.Output()); f > 0 {
		freed += f
		details = append(details, fmt.Sprintf("images: %.1f MB", f))
	}

	// Clear build cache
	res = c.transport.DockerCommand(ctx, "builder prune -af --filter until="+age)
	if f := extractDockerFreed(res.Output()); f > 0 {
		freed += f
		details = append(details, fmt.Sprintf("build cache: %.1f MB", f))
	}

	// Remove unused networks
	c.transport.DockerCommand(ctx, "network prune -f")

	if len(details) > 0 {
		t.Freed = fmt.Sprintf("%.1f MB (%s)", freed, strings.Join(details, ", "))
	} else {
		t.Freed = "0.0 MB"
	}
	return t
}

// --- Go ---

func (c *CleanupCollector) scanGo(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "go"}
	if !c.probe(ctx, "go") {
		return t
	}
	t.Available = true
	res := c.transport.ExecuteUnsafe(ctx, "go env GOCACHE 2>/dev/null")
	if res.Success {
		cachePath := strings.TrimSpace(res.Stdout)
		if cachePath != "" {
			t.SizeMB = c.duSizeMB(ctx, cachePath)
		}
	}
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
	before := c.scanGo(ctx).SizeMB

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

// --- memory ---

// memSwapMB returns (swapTotal, swapUsed) in MB by parsing `free -m`.
func (c *CleanupCollector) memSwapMB(ctx context.Context) (total, used float64) {
	res := c.transport.ExecuteUnsafe(ctx, "free -m | awk '/^Swap:/ {print $2, $3}'")
	if !res.Success {
		return 0, 0
	}
	parts := strings.Fields(strings.TrimSpace(res.Stdout))
	if len(parts) == 2 {
		total, _ = strconv.ParseFloat(parts[0], 64)
		used, _ = strconv.ParseFloat(parts[1], 64)
	}
	return total, used
}

// staleProcsInfo finds orphaned claude/gopls processes (PPID=1, running > 2h).
// Returns list of PIDs and total RSS in KB.
func (c *CleanupCollector) staleProcsInfo(ctx context.Context) (pids []string, rssKB float64) {
	// PPID=1 means reparented to init after parent shell/windsurf died.
	// etimes > 7200 = running more than 2 hours.
	res := c.transport.ExecuteUnsafe(ctx, `ps -eo pid,ppid,etimes,rss,comm --no-headers | awk '$2==1 && $3>7200 && ($5=="claude" || $5=="gopls") {print $1, $4}'`)
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return nil, 0
	}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		pids = append(pids, parts[0])
		rss, _ := strconv.ParseFloat(parts[1], 64)
		rssKB += rss
	}
	return pids, rssKB
}

// scanMemory reports swap usage and stale orphaned process memory.
func (c *CleanupCollector) scanMemory(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "memory", Available: true}

	swapTotal, swapUsed := c.memSwapMB(ctx)
	pids, rssKB := c.staleProcsInfo(ctx)

	t.SizeMB = swapUsed + rssKB/1024

	var parts []string
	if swapTotal > 0 && swapUsed > 0 {
		parts = append(parts, fmt.Sprintf("swap %g/%g MB", swapUsed, swapTotal))
	}
	if len(pids) > 0 {
		parts = append(parts, fmt.Sprintf("%d stale procs %.0f MB", len(pids), rssKB/1024))
	}
	if len(parts) > 0 {
		t.Freed = strings.Join(parts, ", ")
	}
	return t
}

// cleanMemory kills stale orphaned processes and flushes swap.
func (c *CleanupCollector) cleanMemory(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "memory", Available: true}
	var freedMB float64
	var actions []string

	// 1. Kill stale orphaned claude/gopls sessions.
	pids, rssKB := c.staleProcsInfo(ctx)
	if len(pids) > 0 {
		c.transport.ExecuteUnsafe(ctx, "kill -9 "+strings.Join(pids, " ")+" 2>/dev/null")
		freed := rssKB / 1024
		freedMB += freed
		actions = append(actions, fmt.Sprintf("killed %d stale procs: %.0f MB", len(pids), freed))
	}

	// 2. Flush swap if configured in /etc/fstab and currently in use.
	swapTotal, swapUsed := c.memSwapMB(ctx)
	if swapTotal > 0 && swapUsed > 0 {
		hasFstab := c.transport.ExecuteUnsafe(ctx, "grep -q 'swap' /etc/fstab && echo yes || echo no")
		if hasFstab.Success && strings.TrimSpace(hasFstab.Stdout) == "yes" {
			res := c.transport.ExecuteUnsafe(ctx, "sudo swapoff -a && sudo swapon -a")
			if res.Success {
				freedMB += swapUsed
				actions = append(actions, fmt.Sprintf("flushed swap: %.0f MB", swapUsed))
			} else {
				actions = append(actions, "swap flush failed: "+strings.TrimSpace(res.Stderr))
			}
		}
	}

	if len(actions) > 0 {
		t.Freed = fmt.Sprintf("%.0f MB (%s)", freedMB, strings.Join(actions, "; "))
	} else {
		t.Freed = "0 MB (nothing to clean)"
	}
	return t
}

// duSizeMB runs du -sm on a path and returns size in MB.
func (c *CleanupCollector) duSizeMB(ctx context.Context, path string) float64 {
	res := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("du -sm %s 2>/dev/null", path))
	if !res.Success {
		return 0
	}
	return parseDuMB(res.Stdout)
}
