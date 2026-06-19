package env

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// List returns a comma-separated env var as a trimmed string slice.
// Empty entries are dropped. Returns nil if not set and def is "".
func List(key, def string) []string {
	v := Str(key, def)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Int64List returns a comma-separated list of int64 values.
// Non-numeric entries are silently skipped. Returns nil if not set.
func Int64List(key string) []int64 {
	v := getenv(key)
	if v == "" {
		return nil
	}
	var result []int64
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			result = append(result, n)
		}
	}
	return result
}

// Map returns a comma-separated list of key:value pairs as a map.
// Format: "k1:v1,k2:v2". Entries without ":" are silently skipped.
// Whitespace is trimmed from keys and values. Returns nil if not set.
func Map(key, def string) map[string]string {
	v := Str(key, def)
	if v == "" {
		return nil
	}
	m := make(map[string]string)
	for _, pair := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		m[k] = strings.TrimSpace(val)
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// URL returns the env var parsed as a URL, or parsed def if not set/invalid.
// Returns nil if both the variable and def are empty.
func URL(key string, def string) *url.URL {
	v := Str(key, def)
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		if def != "" {
			u, _ = url.Parse(def)
			return u
		}
		return nil
	}
	return u
}

// URLE is like URL but returns a ParseError if the variable is set but not a valid URL.
// If the variable is not set, returns the parsed def.
func URLE(key string, def string) (*url.URL, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		if def == "" {
			return nil, nil
		}
		u, _ := url.Parse(def)
		return u, nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return nil, &ParseError{Key: key, Value: v, Type: "url", Err: err}
	}
	return u, nil
}

// File reads a file path from the env var and returns its trimmed contents.
// Returns def if not set or unreadable. Useful for Docker/K8s secrets.
func File(key, def string) string {
	path := getenv(key)
	if path == "" {
		return def
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	return strings.TrimRight(string(data), "\n")
}

// FileE is like File but returns an error if not set or unreadable.
func FileE(key string) (string, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return "", &NotSetError{Key: key}
	}
	data, err := os.ReadFile(v)
	if err != nil {
		return "", &ParseError{Key: key, Value: v, Type: "file", Err: err}
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// Expand returns the env var with ${VAR} references expanded via DefaultSource.
// Returns def if the variable is not set.
func Expand(key, def string) string {
	v := Str(key, def)
	if v == "" {
		return ""
	}
	return os.Expand(v, getenv)
}

// Base64 returns the environment variable decoded from standard base64.
// Returns def if not set or invalid.
func Base64(key string, def []byte) []byte {
	v := getenv(key)
	if v == "" {
		return def
	}
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return def
	}
	return data
}

// Base64E is like Base64 but returns a ParseError on invalid base64.
func Base64E(key string, def []byte) ([]byte, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "base64", Err: err}
	}
	return data, nil
}

// Hex returns the environment variable decoded from hex.
// Returns def if not set or invalid.
func Hex(key string, def []byte) []byte {
	v := getenv(key)
	if v == "" {
		return def
	}
	data, err := hex.DecodeString(v)
	if err != nil {
		return def
	}
	return data
}

// HexE is like Hex but returns a ParseError on invalid hex.
func HexE(key string, def []byte) ([]byte, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	data, err := hex.DecodeString(v)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "hex", Err: err}
	}
	return data, nil
}
