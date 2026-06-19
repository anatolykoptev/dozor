// Package env provides typed access to environment variables with defaults.
// Zero external dependencies. Designed to replace duplicated env/envInt/envList
// helpers found across go-* services.
package env

import "os"

// Source provides environment variable lookups. Replace DefaultSource
// for testing or to read from alternative sources.
type Source interface {
	Lookup(key string) (string, bool)
}

type osSource struct{}

func (osSource) Lookup(key string) (string, bool) { return os.LookupEnv(key) }

// DefaultSource is the global source for all env functions.
// Defaults to OSSource (reads from os.LookupEnv).
// Replace with MapSource in tests for parallel-safe, isolated testing.
var DefaultSource Source = osSource{}

type mapSource map[string]string

// MapSource returns a Source backed by a map. Use in tests:
//
//	env.DefaultSource = env.MapSource(map[string]string{"KEY": "value"})
func MapSource(m map[string]string) Source {
	return mapSource(m)
}

func (ms mapSource) Lookup(key string) (string, bool) {
	v, ok := ms[key]
	return v, ok
}

// getenv returns the value from DefaultSource, or "" if not set.
func getenv(key string) string {
	v, _ := DefaultSource.Lookup(key)
	return v
}

// Lookup returns the value of the environment variable and whether it was set.
// Unlike Str, it distinguishes between "not set" and "set to empty string".
func Lookup(key string) (string, bool) {
	return DefaultSource.Lookup(key)
}

// Exists reports whether the environment variable is set (even if empty).
func Exists(key string) bool {
	_, ok := DefaultSource.Lookup(key)
	return ok
}

// Required returns the value of the environment variable named by key.
// Returns NotSetError if the variable is not set or is empty.
// Use this for variables that must be configured (e.g. DATABASE_URL).
func Required(key string) (string, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return "", &NotSetError{Key: key}
	}
	return v, nil
}

// Str returns the value of the environment variable named by key,
// or def if the variable is not set or empty.
func Str(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
