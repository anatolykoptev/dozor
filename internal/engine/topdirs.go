package engine

import (
	"context"
	"sort"
	"strings"
)

// bytesPerMB is 1024*1024, used to convert ParseSizeMB output to bytes.
const duBytesPerMB = 1024 * 1024

// DirSize holds information about a single directory's disk usage.
type DirSize struct {
	Path  string // absolute path
	Size  string // human-readable size string (e.g. "5.2G")
	Bytes int64  // parsed bytes, for sorting and threshold comparisons
}

// TopDirs returns the N largest direct subdirectories of root, sorted by size descending.
// It runs `du -h --max-depth=1 --threshold=100M <root>` and parses the output.
// Returns up to n entries. Errors are non-fatal — returns whatever entries could be parsed.
func TopDirs(ctx context.Context, transport Transporter, root string, n int) ([]DirSize, error) {
	cmd := "du -h --max-depth=1 --threshold=100M " + root + " 2>/dev/null"
	res := transport.ExecuteUnsafe(ctx, cmd)
	return parseDuOutput(res.Stdout, n), nil
}

// parseDuOutput parses `du -h` tab-separated output into a sorted (desc) slice of DirSize.
// Skips "." (root summary) and empty lines. Returns up to n entries.
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
		dirs = append(dirs, DirSize{Path: path, Size: size, Bytes: bytes})
	}

	// Sort descending by bytes so callers see the biggest dirs first.
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Bytes > dirs[j].Bytes })

	if n > 0 && len(dirs) > n {
		dirs = dirs[:n]
	}
	return dirs
}
