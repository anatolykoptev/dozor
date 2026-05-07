package engine

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
)

// dfFreeRootMB returns the free MB on / via `df -BM --output=avail /`.
// Returns 0 if df fails or its output cannot be parsed.
func (c *CleanupCollector) dfFreeRootMB(ctx context.Context) float64 {
	res := c.transport.ExecuteUnsafe(ctx, "df -BM --output=avail / 2>/dev/null")
	if !res.Success {
		return 0
	}
	return parseDfAvailMB(res.Stdout)
}

// parseDfAvailMB parses `df -BM --output=avail /` output.
// Expected format (two lines):
//
//	Avail
//	12345M
//
// Returns 0 on any parse error.
func parseDfAvailMB(output string) float64 {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0
	}
	// Second line is the value; strip trailing "M".
	raw := strings.TrimSuffix(strings.TrimSpace(lines[1]), "M")
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return v
}

// measureFreedMB calls fn between two df measurements and returns clamp(after−before, 0).
// Logs a warning when another process wrote during the window (negative delta).
func (c *CleanupCollector) measureFreedMB(ctx context.Context, fn func()) float64 {
	before := c.dfFreeRootMB(ctx)
	fn()
	after := c.dfFreeRootMB(ctx)
	delta := after - before
	if delta < 0 {
		slog.WarnContext(ctx, "cleanup: df delta negative — another process wrote during window",
			"before_mb", before, "after_mb", after, "delta_mb", delta)
		return 0
	}
	return delta
}
