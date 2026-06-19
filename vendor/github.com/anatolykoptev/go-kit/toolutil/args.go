// Package toolutil provides argument extraction helpers for MCP tool handlers.
// Works with map[string]any argument maps from JSON-decoded tool inputs.
package toolutil

import "github.com/anatolykoptev/go-kit/strutil"

// ArgString extracts a string argument from a tool args map.
func ArgString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// ArgStringDefault extracts a string argument with a default value.
func ArgStringDefault(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

// ArgInt extracts an integer argument, handling JSON float64 and native int types.
func ArgInt(args map[string]any, key string) int {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// ArgFloat64 extracts a float64 argument.
func ArgFloat64(args map[string]any, key string) float64 {
	v, _ := args[key].(float64)
	return v
}

// ArgBool extracts a boolean argument.
func ArgBool(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

// ArgIntSlice converts a []any of JSON numbers to []int.
func ArgIntSlice(arr []any) []int {
	result := make([]int, 0, len(arr))
	for _, v := range arr {
		if f, ok := v.(float64); ok {
			result = append(result, int(f))
		}
	}
	return result
}

// Coalesce returns the first non-empty string from the given values.
func Coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// TruncateStr truncates a string to n runes, appending "..." if truncated.
func TruncateStr(s string, n int) string { return strutil.Truncate(s, n) }
