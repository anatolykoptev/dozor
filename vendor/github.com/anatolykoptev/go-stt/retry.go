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

type circuitBreaker struct {
	mu        sync.Mutex
	failures  int
	maxFails  int
	cooldown  time.Duration
	openUntil time.Time
}

// allow returns true if a request is allowed through the circuit breaker.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.failures >= cb.maxFails {
		if time.Now().Before(cb.openUntil) {
			return false
		}
		// cooldown elapsed — reset and allow
		cb.failures = 0
	}
	return true
}

// recordSuccess resets the failure counter.
func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
}

// recordFailure increments the failure counter and sets the open-until time.
func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	if cb.failures >= cb.maxFails {
		cb.openUntil = time.Now().Add(cb.cooldown)
	}
}

// doWithRetry calls fn with exponential backoff, respecting ctx and the circuit breaker.
// Non-transient errors are returned immediately without retry.
func doWithRetry[T any](ctx context.Context, rc *retryConfig, cb *circuitBreaker, fn func() (T, error)) (T, error) {
	var zero T

	if cb != nil && !cb.allow() {
		return zero, &Error{StatusCode: http.StatusServiceUnavailable, Message: "circuit breaker open"}
	}

	delay := rc.baseDelay
	var lastErr error

	for attempt := range rc.maxAttempts {
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
