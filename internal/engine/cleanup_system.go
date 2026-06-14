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

// isSudoStructurallyUnavailable reports whether an error string from a failed
// "sudo -n" invocation represents a structural impossibility (NoNewPrivileges flag
// set, or /etc/sudo.conf ownership error) rather than a transient or command error.
// In these cases sudo can NEVER succeed in this process — the failure must not be
// reported as an error that escalates to the partial-WARN aggregate.
func isSudoStructurallyUnavailable(errStr string) bool {
	return strings.Contains(errStr, "no new privileges") ||
		strings.Contains(errStr, "sudo: /etc/sudo.conf is owned by uid")
}

// probeSudo runs "sudo -n true" once and caches whether passwordless sudo is
// available in this process. Subsequent calls return the cached result instantly.
// The probe uses ctx only for the first call; later calls ignore ctx.
func (c *CleanupCollector) probeSudo(ctx context.Context) bool {
	c.sudoOnce.Do(func() {
		res := c.transport.ExecuteUnsafe(ctx, "sudo -n true 2>&1")
		if res.Success {
			c.sudoAvail = true
			return
		}
		combined := res.Stdout
		if combined == "" {
			combined = res.Stderr
		}
		if isSudoStructurallyUnavailable(combined) {
			slog.InfoContext(ctx, "cleanAptCache: sudo structurally unavailable (NoNewPrivileges or ownership); apt target disabled for this process",
				slog.String("reason", combined))
		}
		// sudoAvail stays false for any failure
	})
	return c.sudoAvail
}

// tryRunSudo executes a command via "sudo -n" (non-interactive, fails fast if passwordless
// sudo is not configured). Returns the command output and any execution error.
// Callers must have already confirmed sudo is available via probeSudo.
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
//
// Sudo availability is probed once per process lifetime via probeSudo:
//   - If sudo is structurally unavailable (NoNewPrivileges flag / ownership error),
//     the target is returned as unavailable (Available=false, no Error) — it does NOT
//     contribute to the partial-WARN aggregate. This is the expected state when dozor
//     runs under systemd with NoNewPrivileges=yes.
//   - If sudo is available but apt-get clean fails, the Error field is set and the
//     failure IS surfaced as a real error — not silenced.
func (c *CleanupCollector) cleanAptCache(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "apt"}
	if !c.probeSudo(ctx) {
		// Structural unavailability: Available stays false, Error stays empty.
		// appendTarget will not push anything to res.Errors.
		return t
	}
	var execErr string
	freed := c.measureFreedMB(ctx, func() {
		_, err := c.tryRunSudo(ctx, "apt-get", "clean")
		if err != nil {
			execErr = err.Error()
			slog.WarnContext(ctx, "cleanAptCache: apt-get clean failed despite sudo being available",
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
