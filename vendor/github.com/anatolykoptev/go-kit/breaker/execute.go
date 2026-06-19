package breaker

// Execute runs fn through the breaker. Returns ErrOpen when the breaker is open.
// On fn error, records a failure. On success, records a success. Allow/Record are
// called transparently — do NOT mix Execute with manual Allow/Record on the same
// breaker instance unless you know what you're doing.
func Execute[T any](b *Breaker, fn func() (T, error)) (T, error) {
	if !b.Allow() {
		var zero T
		return zero, ErrOpen
	}
	result, err := fn()
	b.Record(err == nil)
	return result, err
}
