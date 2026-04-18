package engine

import (
	"context"
	"fmt"
)

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
	// Use sudo to access permission-restricted subdirectories (snap-private-tmp, etc.)
	res := c.transport.ExecuteUnsafe(ctx, "sudo du -sm /tmp 2>/dev/null || du -sm /tmp 2>/dev/null")
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
	// Exclude go-build from ~/.cache — it is already counted by scanGo via GOCACHE.
	res := c.transport.ExecuteUnsafe(ctx, "du -sm ~/.cache --exclude='go-build' 2>/dev/null")
	if res.Success {
		t.SizeMB = parseDuMB(res.Stdout)
	}
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
