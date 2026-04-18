package engine

import (
	"context"
	"fmt"
	"strings"
)

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
