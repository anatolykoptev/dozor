package env

import (
	"net/url"
	"time"
)

// MustRequired returns the value of the environment variable named by key.
// Panics if the variable is not set or empty. Intended for fail-fast startup validation.
func MustRequired(key string) string {
	v, err := Required(key)
	if err != nil {
		panic(err)
	}
	return v
}

// MustInt is like Int but panics if the variable is set to an invalid integer.
func MustInt(key string, def int) int {
	v, err := IntE(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustInt64 is like Int64 but panics if the variable is set to an invalid int64.
func MustInt64(key string, def int64) int64 {
	v, err := Int64E(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustFloat is like Float but panics if the variable is set to an invalid float64.
func MustFloat(key string, def float64) float64 {
	v, err := FloatE(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustUint is like Uint but panics if the variable is set to an invalid uint.
func MustUint(key string, def uint) uint {
	v, err := UintE(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustUint64 is like Uint64 but panics if the variable is set to an invalid uint64.
func MustUint64(key string, def uint64) uint64 {
	v, err := Uint64E(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustBool is like Bool but panics if the variable is set to an unrecognized value.
func MustBool(key string, def bool) bool {
	v, err := BoolE(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustDuration is like Duration but panics if the variable is set to an invalid duration.
func MustDuration(key string, def time.Duration) time.Duration {
	v, err := DurationE(key, def)
	if err != nil {
		panic(err)
	}
	return v
}

// MustURL is like URL but panics if the variable is set to an invalid URL.
func MustURL(key string, def string) *url.URL {
	v, err := URLE(key, def)
	if err != nil {
		panic(err)
	}
	return v
}
