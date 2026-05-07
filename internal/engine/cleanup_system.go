package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		res := c.transport.ExecuteUnsafe(ctx, "journalctl --vacuum-time="+vacuumTime+" 2>/dev/null")
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
	cmd := fmt.Sprintf("find /tmp -type f -atime +%s -delete 2>/dev/null; echo done", atime)
	freed := c.measureFreedMB(ctx, func() {
		c.transport.ExecuteUnsafe(ctx, cmd)
	})
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
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
	freed := c.measureFreedMB(ctx, func() {
		for _, dir := range staleDirs {
			c.transport.ExecuteUnsafe(ctx, "rm -rf '"+dir+"' 2>/dev/null")
		}
	})
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// --- apt cache ---

// tryRunSudo executes a command via "sudo -n" (non-interactive, fails fast if passwordless
// sudo is not configured). Returns the command output and any execution error.
// A non-zero exit or a stderr containing "sudo:" is treated as a sudo-unavailable error.
func (c *CleanupCollector) tryRunSudo(ctx context.Context, args ...string) (string, error) {
	cmd := "sudo -n " + strings.Join(args, " ") + " 2>&1"
	res := c.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success {
		combined := res.Stderr
		if combined == "" {
			combined = res.Stdout
		}
		return "", fmt.Errorf("sudo -n %s: %s", strings.Join(args, " "), combined)
	}
	return res.Stdout, nil
}

// cleanAptCache cleans /var/cache/apt/archives via "sudo -n apt-get clean".
// If passwordless sudo is not configured the target is skipped (FreedMB=0, Error set).
// This is a WARNING-level target because apt caches can accumulate gigabytes over time
// but rarely require immediate attention.
func (c *CleanupCollector) cleanAptCache(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "apt"}
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		_, err := c.tryRunSudo(ctx, "apt-get", "clean")
		if err != nil {
			execErr = "apt cache skipped: " + err.Error()
			slog.InfoContext(ctx, "cleanAptCache: skipping — passwordless sudo not configured or apt-get failed",
				slog.String("error", err.Error()))
		}
	})
	if execErr != "" {
		t.Error = execErr
		return t
	}
	t.Available = true
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}
