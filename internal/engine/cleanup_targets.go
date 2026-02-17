package engine

import (
	"context"
	"fmt"
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
	res := c.transport.DockerCommand(ctx, "image prune -af --filter until="+age)
	freed := extractDockerFreed(res.Output())
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

// duSizeMB runs du -sm on a path and returns size in MB.
func (c *CleanupCollector) duSizeMB(ctx context.Context, path string) float64 {
	res := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("du -sm %s 2>/dev/null", path))
	if !res.Success {
		return 0
	}
	return parseDuMB(res.Stdout)
}
