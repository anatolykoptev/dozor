package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	// kilobytesPerMegabyte is the conversion factor from kilobytes to megabytes.
	kilobytesPerMegabyte = 1024
)

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

	t.SizeMB = swapUsed + rssKB/kilobytesPerMegabyte

	var parts []string
	if swapTotal > 0 && swapUsed > 0 {
		parts = append(parts, fmt.Sprintf("swap %g/%g MB", swapUsed, swapTotal))
	}
	if len(pids) > 0 {
		parts = append(parts, fmt.Sprintf("%d stale procs %.0f MB", len(pids), rssKB/kilobytesPerMegabyte))
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
		freed := rssKB / kilobytesPerMegabyte
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
