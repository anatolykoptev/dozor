package env

import (
	"strconv"
	"strings"
	"time"
)

// Int returns the environment variable as an int, or def if not set or invalid.
func Int(key string, def int) int {
	if v := getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// IntE is like Int but returns a ParseError if the variable is set but not a valid integer.
// If the variable is not set, returns (def, nil).
func IntE(key string, def int) (int, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "int", Err: err}
	}
	return n, nil
}

// Int64 returns the environment variable as int64, or def if not set or invalid.
func Int64(key string, def int64) int64 {
	if v := getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Int64E is like Int64 but returns a ParseError if the variable is set but not a valid int64.
func Int64E(key string, def int64) (int64, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "int64", Err: err}
	}
	return n, nil
}

// Float returns the environment variable as float64, or def if not set or invalid.
func Float(key string, def float64) float64 {
	if v := getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// FloatE is like Float but returns a ParseError if the variable is set but not a valid float64.
func FloatE(key string, def float64) (float64, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "float64", Err: err}
	}
	return f, nil
}

// Uint returns the environment variable as a uint, or def if not set or invalid.
func Uint(key string, def uint) uint {
	if v := getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, strconv.IntSize); err == nil {
			return uint(n)
		}
	}
	return def
}

// UintE is like Uint but returns a ParseError if the variable is set but not a valid uint.
func UintE(key string, def uint) (uint, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, strconv.IntSize)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "uint", Err: err}
	}
	return uint(n), nil
}

// Uint64 returns the environment variable as uint64, or def if not set or invalid.
func Uint64(key string, def uint64) uint64 {
	if v := getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Uint64E is like Uint64 but returns a ParseError if the variable is set but not a valid uint64.
func Uint64E(key string, def uint64) (uint64, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "uint64", Err: err}
	}
	return n, nil
}

// Bool returns the environment variable as a bool, or def if not set.
// Truthy: "true", "1", "yes" (case-insensitive).
// Falsy: "false", "0", "no" (case-insensitive).
// Anything else returns def.
func Bool(key string, def bool) bool {
	v := getenv(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}

// BoolE is like Bool but returns a ParseError if the variable is set
// to an unrecognized value (not true/1/yes/false/0/no).
func BoolE(key string, def bool) (bool, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return def, &ParseError{Key: key, Value: v, Type: "bool"}
	}
}

// Duration returns the environment variable parsed as a duration.
// Accepts Go duration strings ("5s", "100ms", "2m30s") and float seconds ("3.5" -> 3.5s).
// Returns def if not set or invalid.
func Duration(key string, def time.Duration) time.Duration {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def
	}
	// Try Go duration format first.
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Fall back to float seconds for backward compat.
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second))
	}
	return def
}

// DurationE is like Duration but returns a ParseError if the variable is set
// but cannot be parsed. Accepts Go duration strings ("5s", "100ms", "2m30s")
// and falls back to float seconds ("3.5" -> 3.5s) for backward compatibility.
func DurationE(key string, def time.Duration) (time.Duration, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	// Try Go duration format first ("5s", "100ms", "2m30s").
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	// Fall back to float seconds for backward compat ("3.5" -> 3.5s).
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return def, &ParseError{Key: key, Value: v, Type: "duration"}
}
