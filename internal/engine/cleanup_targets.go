package engine

import (
	"context"
	"fmt"
)

// duSizeMB runs `du -sm` on a path and returns size in MB.
// Shared helper used by multiple cleanup target scanners.
func (c *CleanupCollector) duSizeMB(ctx context.Context, path string) float64 {
	res := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("du -sm %s 2>/dev/null", path))
	if !res.Success {
		return 0
	}
	return parseDuMB(res.Stdout)
}
