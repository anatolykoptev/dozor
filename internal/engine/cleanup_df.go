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
//
// Primary format (util-linux ≥ 2.25 with --output support, two lines):
//
//	Avail
//	12345M
//
// Fallback format (older util-linux without --output=avail support, six columns):
//
//	Filesystem 1M-blocks Used Available Use% Mounted on
//	/dev/sda1  100000M   50000M 45000M  53%  /
//
// In fallback mode the function scans the second line for the first token that
// ends with "M" and is preceded by at least one digit — this is the "Available"
// column regardless of column order differences across distributions.
//
// Returns 0 on any parse error.
func parseDfAvailMB(output string) float64 {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0
	}

	// Primary: first line must be exactly "Avail" (--output=avail format).
	if strings.TrimSpace(lines[0]) == "Avail" {
		raw := strings.TrimSuffix(strings.TrimSpace(lines[1]), "M")
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0
		}
		return v
	}

	// Fallback: traditional 6-column output. Locate the "Available" column by
	// scanning the header, then read the same column index from the data line.
	headerFields := strings.Fields(lines[0])
	dataFields := strings.Fields(strings.TrimSpace(lines[1]))
	for i, h := range headerFields {
		if h == "Available" && i < len(dataFields) {
			raw := strings.TrimSuffix(dataFields[i], "M")
			if v, err := strconv.ParseFloat(raw, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

// measureFreedMB calls fn between two df measurements and returns clamp(after−before, 0).
// Concurrent writes during the cleanup window will reduce the apparent freed value below
// the true amount; the result is a lower bound, not an exact figure.
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
