package engine

import (
	"context"
	"sort"
	"strings"
)

// duBytesPerMB is 1024*1024, used to convert ParseSizeMB output to bytes.
// A second copy lives in cmd/dozor/gateway_remediate_cooldown.go (bytesPerMB) —
// two copies is fine per project rule (3 copies triggers extraction).
const duBytesPerMB = 1024 * 1024

// duThresholdBytes is the minimum size for a directory to appear in TopDirs results.
// Matches the old GNU-only --threshold=100M flag — now enforced in Go so the
// command works on both GNU coreutils and BSD/macOS du.
const duThresholdBytes = 100 * 1024 * 1024

// DirSize holds information about a single directory's disk usage.
type DirSize struct {
	Path  string // absolute path
	Size  string // human-readable size string (e.g. "5.2G")
	Bytes int64  // parsed bytes, for sorting and threshold comparisons
}

// TopDirs returns the N largest direct subdirectories of root, sorted by size descending.
// It runs `LC_ALL=C du -h --max-depth=1 <root>` and filters entries < 100 MB in Go.
// LC_ALL=C ensures dot decimal separators regardless of host locale.
// root is shell-quoted to guard against paths with spaces or special characters.
// Returns up to n entries. Errors are non-fatal — returns whatever entries could be parsed.
func TopDirs(ctx context.Context, transport Transporter, root string, n int) ([]DirSize, error) {
	quotedRoot := "'" + strings.ReplaceAll(root, "'", "'\\''") + "'"
	cmd := "LC_ALL=C du -h --max-depth=1 " + quotedRoot + " 2>/dev/null | sort -h"
	res := transport.ExecuteUnsafe(ctx, cmd)
	return parseDuOutput(res.Stdout, n), nil
}

// parseDuOutput parses `du -h` tab-separated output into a sorted (desc) slice of DirSize.
// Skips "." (root summary), empty lines, and entries smaller than duThresholdBytes (100 MB).
// Returns up to n entries.
func parseDuOutput(raw string, n int) []DirSize {
	var dirs []DirSize
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// du format: "<size>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		size := strings.TrimSpace(line[:tab])
		path := strings.TrimSpace(line[tab+1:])
		// Skip "." (root summary) and "total" lines emitted by some du versions.
		if path == "." || path == "total" {
			continue
		}
		bytes := int64(ParseSizeMB(size) * duBytesPerMB)
		// Filter below threshold in Go — avoids GNU-only --threshold flag.
		// Unparseable sizes (e.g. comma-decimal locale artefacts) produce bytes=0 and are
		// also dropped here, which is the correct behaviour with LC_ALL=C guarding the call.
		if bytes < duThresholdBytes {
			continue
		}
		dirs = append(dirs, DirSize{Path: path, Size: size, Bytes: bytes})
	}

	// Sort descending by bytes so callers see the biggest dirs first.
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Bytes > dirs[j].Bytes })

	if n > 0 && len(dirs) > n {
		dirs = dirs[:n]
	}
	return dirs
}
