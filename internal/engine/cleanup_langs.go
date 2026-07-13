package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// cleanSccache removes the sccache on-disk cache. It resolves the directory
// via resolveSccacheDir's priority order and, if a candidate exists, rm -rf's
// it. If no candidate exists the target is returned as unavailable (no
// error). sccache rebuilds its cache automatically on the next compile —
// nuking the cache costs only a cold-rebuild cycle, which is acceptable
// under disk pressure.
//
// Called only at the CRITICAL/Error tier (see AutoRemediateDisk's doc comment):
// sccache-shared sits at its configured SCCACHE_CACHE_SIZE cap by design
// (LRU-managed) — nuking it at a lower tier would routinely wipe the fleet's
// build-cache accelerator for a non-emergency disk level.
func (c *CleanupCollector) cleanSccache(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "sccache"}
	dir, ok := c.resolveSccacheDir(ctx)
	if !ok {
		// No candidate directory exists — skip silently.
		return t
	}
	t.Available = true
	freed := c.measureFreedMB(ctx, func() {
		c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("rm -rf '%s' 2>/dev/null", dir))
	})
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// resolveSccacheDir picks the sccache cache directory to clean, in priority
// order, resolving every candidate to a real absolute path in Go before it
// ever reaches a shell string — a literal "~" inside a single-quoted shell
// command is NOT expanded by the shell (verified live: `du -sm
// '~/.cache/sccache'` → "cannot access '~/.cache/sccache': No such file or
// directory"), which previously made the home-dir fallback always broken.
//
//  1. SCCACHE_DIR env var, if set — an explicit operator override always wins.
//  2. cargoRoot+"/sccache-shared" — the fleet-standard shared cache path
//     (cargoRoot / the "sccache-shared" denylist entry are cleanup_cargo.go's
//     existing convention for this mount), used when SCCACHE_DIR is unset and
//     cargoRoot is actually mounted.
//  3. ~/.cache/sccache, resolved via os.UserHomeDir() — last-resort fallback
//     for hosts without the shared mount.
//
// Each candidate is confirmed to exist via du -sm before being returned;
// resolveSccacheDir falls through to the next candidate on failure. Returns
// ok=false when no candidate exists.
func (c *CleanupCollector) resolveSccacheDir(ctx context.Context) (string, bool) {
	if dir := os.Getenv("SCCACHE_DIR"); dir != "" {
		return c.probeSccacheDir(ctx, dir)
	}
	if c.cargoMountAvailable(ctx) {
		if dir, ok := c.probeSccacheDir(ctx, cargoRoot+"/sccache-shared"); ok {
			return dir, true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return c.probeSccacheDir(ctx, filepath.Join(home, ".cache", "sccache"))
}

// probeSccacheDir confirms dir exists via du -sm (succeeds only if the dir
// exists) and returns it as the resolved candidate on success.
func (c *CleanupCollector) probeSccacheDir(ctx context.Context, dir string) (string, bool) {
	res := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("du -sm '%s' 2>/dev/null", dir))
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return "", false
	}
	return dir, true
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
