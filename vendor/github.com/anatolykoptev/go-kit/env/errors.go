package env

import "fmt"

// NotSetError indicates a required environment variable is not set or empty.
type NotSetError struct {
	Key string
}

func (e *NotSetError) Error() string {
	return fmt.Sprintf("env: %q is not set", e.Key)
}

// ParseError indicates an environment variable was set but could not be
// parsed as the expected type.
type ParseError struct {
	Key   string
	Value string
	Type  string
	Err   error // underlying strconv/parse error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("env: cannot parse %q value %q as %s", e.Key, e.Value, e.Type)
}

func (e *ParseError) Unwrap() error {
	return e.Err
}
