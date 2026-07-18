package stt

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

type retryConfig struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// cbState is the explicit state of the circuit breaker.
type cbState int

const (
	cbClosed   cbState = iota // normal operation, requests allowed
	cbOpen                    // tripped, all requests rejected until cooldown elapses
	cbHalfOpen                // cooldown elapsed, a single probe request is allowed
)

type circuitBreaker struct {
	mu            sync.Mutex
	failures      int
	maxFails      int
	cooldown      time.Duration
	openUntil     time.Time
	state         cbState
	probeInFlight bool // true while a half-open probe request is in progress
	now           func() time.Time
}

// setClock replaces the clock function used for cooldown timing.
// Used in tests for deterministic behavior without real sleeps.
func (cb *circuitBreaker) setClock(now func() time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.now = now
}

// clock returns the current time, defaulting to time.Now if no clock
// has been injected via setClock.
func (cb *circuitBreaker) clock() time.Time {
	if cb.now != nil {
		return cb.now()
	}
	return time.Now()
}

// allow returns true if a request is allowed through the circuit breaker.
// In half-open state, exactly one request (the probe) is allowed; all others
// are rejected until the probe completes and transitions the state.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case cbClosed:
		return true
	case cbOpen:
		if cb.clock().Before(cb.openUntil) {
			return false
		}
		// Cooldown elapsed — transition to half-open and allow a single probe.
		cb.state = cbHalfOpen
		cb.probeInFlight = true
		return true
	case cbHalfOpen:
		if cb.probeInFlight {
			return false // a probe is already in flight
		}
		// No probe in flight (e.g. previous probe's result was recorded but
		// state wasn't transitioned) — allow a new probe.
		cb.probeInFlight = true
		return true
	}
	return false
}

// recordSuccess resets the failure counter. In half-open state, a successful
// probe closes the circuit.
func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == cbHalfOpen {
		// Probe succeeded — close the circuit.
		cb.state = cbClosed
		cb.failures = 0
		cb.probeInFlight = false
		return
	}
	cb.failures = 0
}

// recordFailure increments the failure counter and opens the circuit when
// failures reach maxFails. In half-open state, a failed probe re-opens the
// circuit with a fresh cooldown.
func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == cbHalfOpen {
		// Probe failed — re-open the circuit.
		cb.state = cbOpen
		cb.openUntil = cb.clock().Add(cb.cooldown)
		cb.probeInFlight = false
		return
	}
	cb.failures++
	if cb.failures >= cb.maxFails {
		cb.state = cbOpen
		cb.openUntil = cb.clock().Add(cb.cooldown)
	}
}

// doWithRetry calls fn with exponential backoff, respecting ctx and the circuit breaker.
// The circuit breaker is checked before EACH attempt so that a breaker opened mid-retry
// (by a concurrent request or by a previous attempt's failure) blocks subsequent attempts.
// Non-transient errors are returned immediately without retry.
func doWithRetry[T any](ctx context.Context, rc *retryConfig, cb *circuitBreaker, fn func() (T, error)) (T, error) {
	var zero T

	delay := rc.baseDelay
	if delay > rc.maxDelay {
		delay = rc.maxDelay
	}
	var lastErr error

	for attempt := range rc.maxAttempts {
		// Check the circuit breaker before every attempt, not just once before the loop.
		// This ensures a breaker that opens mid-retry (by this loop or a concurrent
		// request) blocks subsequent attempts instead of stampeding through.
		if cb != nil && !cb.allow() {
			return zero, &Error{StatusCode: http.StatusServiceUnavailable, Message: "circuit breaker open"}
		}

		result, err := fn()
		if err == nil {
			if cb != nil {
				cb.recordSuccess()
			}
			return result, nil
		}

		lastErr = err

		var sttErr *Error
		if !errors.As(err, &sttErr) || !sttErr.IsTransient() {
			if cb != nil {
				cb.recordFailure()
			}
			return zero, err
		}

		// Transient error: record failure, then backoff unless last attempt.
		if cb != nil {
			cb.recordFailure()
		}

		if attempt == rc.maxAttempts-1 {
			break
		}

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2 //nolint:mnd
		if delay > rc.maxDelay {
			delay = rc.maxDelay
		}
	}

	return zero, lastErr
}
