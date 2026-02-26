package engine

import (
	"fmt"
	"strconv"
	"strings"
)


// ParseSizeMB parses size strings like "1.5G", "500M", "100K", "(1.5 GB)".
func ParseSizeMB(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "()")
	s = strings.ToUpper(s)

	var numStr string
	var unit string
	for i, c := range s {
		if (c >= '0' && c <= '9') || c == '.' {
			numStr += string(c) //nolint:perfsprint
		} else {
			unit = strings.TrimSpace(s[i:])
			break
		}
	}
	if numStr == "" {
		return 0
	}
	val, _ := strconv.ParseFloat(numStr, 64)

	switch {
	case strings.HasPrefix(unit, "T"):
		return val * megabytesPerGigabyte * kilobytesPerMegabyte
	case strings.HasPrefix(unit, "G"):
		return val * megabytesPerGigabyte
	case strings.HasPrefix(unit, "M"):
		return val
	case strings.HasPrefix(unit, "K"):
		return val / kilobytesPerMegabyte
	case strings.HasPrefix(unit, "B"):
		return val / (megabytesPerGigabyte * kilobytesPerMegabyte)
	default:
		return val
	}
}

// ParseSizeGB parses human-readable sizes like "15G", "500M", "1.5T" to GB.
func ParseSizeGB(s string) float64 {
	return ParseSizeMB(s) / megabytesPerGigabyte
}

// ParseDockerMemoryMB parses docker stats memory format like "123.4MiB / 1.5GiB".
func ParseDockerMemoryMB(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 0 {
		return 0
	}
	used := strings.TrimSpace(parts[0])
	used = strings.ToLower(used)

	var val float64
	switch {
	case strings.HasSuffix(used, "gib"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(used, "gib"), 64)
		val = v * megabytesPerGigabyte
	case strings.HasSuffix(used, "mib"):
		val, _ = strconv.ParseFloat(strings.TrimSuffix(used, "mib"), 64)
	case strings.HasSuffix(used, "kib"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(used, "kib"), 64)
		val = v / kilobytesPerMegabyte
	case strings.HasSuffix(used, "b"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(used, "b"), 64)
		val = v / (megabytesPerGigabyte * kilobytesPerMegabyte)
	}
	return val
}

// BytesToMB converts a byte count string to megabytes.
// Stops at the first non-digit character (lenient for trailing whitespace).
func BytesToMB(s string) (float64, bool) {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		} else {
			break
		}
	}
	if n <= 0 {
		return 0, false
	}
	return float64(n) / (megabytesPerGigabyte * kilobytesPerMegabyte), true
}

// FormatBytesMB converts bytes string to human-readable MB string.
func FormatBytesMB(s string) (string, bool) {
	mb, ok := BytesToMB(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%.1f MB", mb), true
}
