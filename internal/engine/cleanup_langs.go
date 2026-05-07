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
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		res := c.transport.ExecuteUnsafe(ctx, "go clean -cache 2>/dev/null")
		if !res.Success {
			execErr = res.Stderr
		}
	})
	if execErr != "" {
		t.Error = execErr
		return t
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
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
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		res := c.transport.ExecuteUnsafe(ctx, "npm cache clean --force 2>/dev/null")
		if !res.Success {
			execErr = res.Stderr
		}
	})
	if execErr != "" {
		t.Error = execErr
		return t
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
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
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		res := c.transport.ExecuteUnsafe(ctx, "uv cache clean 2>/dev/null")
		if !res.Success {
			execErr = res.Stderr
		}
	})
	if execErr != "" {
		t.Error = execErr
		return t
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
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
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		res := c.transport.ExecuteUnsafe(ctx, "pip cache purge 2>/dev/null || pip3 cache purge 2>/dev/null")
		if !res.Success {
			execErr = res.Stderr
		}
	})
	if execErr != "" {
		t.Error = execErr
		return t
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// --- sccache ---

// cleanSccache removes the sccache on-disk cache at ~/.cache/sccache.
// If the directory does not exist the target is returned as unavailable (no error).
// sccache rebuilds its cache automatically on the next compile — nuking the cache
// costs only a cold-rebuild cycle, which is acceptable under disk pressure.
func (c *CleanupCollector) cleanSccache(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "sccache"}
	// Probe: du -sm succeeds only if the dir exists.
	res := c.transport.ExecuteUnsafe(ctx, "du -sm ~/.cache/sccache 2>/dev/null")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		// Directory absent — skip silently.
		return t
	}
	t.Available = true
	freed := c.measureFreedMB(ctx, func() {
		c.transport.ExecuteUnsafe(ctx, "rm -rf ~/.cache/sccache 2>/dev/null")
	})
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// --- npm / yarn caches ---

// cleanNpmYarn removes npm and yarn package manager caches:
//   - ~/.npm/_cacache  — npm download cache (often 1-5 GB on active JS dev boxes)
//   - ~/.cache/yarn    — Yarn 1.x offline mirror / cache
//
// Only caches are removed — not globally installed packages.
func (c *CleanupCollector) cleanNpmYarn(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "npm_yarn", Available: true}
	cacheDirs := []string{
		"~/.npm/_cacache",
		"~/.cache/yarn",
	}
	freed := c.measureFreedMB(ctx, func() {
		for _, dir := range cacheDirs {
			c.transport.ExecuteUnsafe(ctx, "rm -rf '"+dir+"' 2>/dev/null")
		}
	})
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}
