package breaker

import "sync/atomic"

// ExecutePanicRecovered counts panics recovered (and re-panicked) from the
// Execute call path. Incremented before the re-panic so monitoring can alert on
// panics flowing through Execute even though the breaker itself stays healthy.
//
// Read with atomic.LoadInt64.
var ExecutePanicRecovered atomic.Int64

// Execute runs fn through the breaker. Returns ErrOpen when the breaker is open.
// On fn error, records a failure. On success, records a success. Allow/Record are
// called transparently — do NOT mix Execute with manual Allow/Record on the same
// breaker instance unless you know what you're doing.
//
// Execute is panic-safe: if fn panics, the deferred recover records a failure
// (releasing any held half-open probe slot) and then re-panics so the caller's
// stack is preserved. Without this guard a panicking fn would skip Record and
// wedge the breaker in half-open state forever (halfOpenInFlight never released).
func Execute[T any](b *Breaker, fn func() (T, error)) (result T, err error) {
	if !b.Allow() {
		var zero T
		return zero, ErrOpen
	}
	defer func() {
		if r := recover(); r != nil {
			ExecutePanicRecovered.Add(1)
			b.Record(false)
			panic(r) // re-panic after recording — preserve original stack
		}
	}()
	result, err = fn()
	b.Record(err == nil)
	return result, err
}
