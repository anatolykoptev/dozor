package engine

import (
	"errors"
	"log/slog"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
)

// CBState represents the circuit breaker state.
type CBState = circuitbreaker.State

const (
	CBClosed   = circuitbreaker.ClosedState
	CBOpen     = circuitbreaker.OpenState
	CBHalfOpen = circuitbreaker.HalfOpenState
)

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreaker wraps failsafe-go circuit breaker with a dozor-compatible API.
type CircuitBreaker struct {
	name string
	cb   circuitbreaker.CircuitBreaker[any]
}

// NewCircuitBreaker creates a circuit breaker with the given name, failure
// threshold, and reset timeout (delay before transitioning to half-open).
func NewCircuitBreaker(name string, threshold int, resetTimeout time.Duration) *CircuitBreaker {
	if threshold < 1 {
		threshold = 1
	}
	cb := circuitbreaker.NewBuilder[any]().
		WithFailureThreshold(uint(threshold)). //nolint:gosec // threshold is validated >= 1 above
		WithDelay(resetTimeout).
		OnClose(func(_ circuitbreaker.StateChangedEvent) {
			slog.Info("circuit breaker closed", slog.String("target", name))
		}).
		OnOpen(func(_ circuitbreaker.StateChangedEvent) {
			slog.Info("circuit breaker opened", slog.String("target", name))
		}).
		OnHalfOpen(func(_ circuitbreaker.StateChangedEvent) {
			slog.Info("circuit breaker half-open", slog.String("target", name))
		}).
		Build()
	return &CircuitBreaker{name: name, cb: cb}
}

// Allow checks if a call should proceed. Acquires a permit that is released
// on RecordSuccess or RecordFailure.
func (cb *CircuitBreaker) Allow() bool {
	return cb.cb.TryAcquirePermit()
}

// RecordSuccess records a successful call.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.cb.RecordSuccess()
}

// RecordFailure records a failed call.
func (cb *CircuitBreaker) RecordFailure() {
	cb.cb.RecordFailure()
}

// Execute runs fn if the circuit allows it, recording success/failure.
// Returns ErrCircuitOpen if the circuit is open.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if !cb.Allow() {
		return ErrCircuitOpen
	}
	err := fn()
	if err != nil {
		cb.RecordFailure()
	} else {
		cb.RecordSuccess()
	}
	return err
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CBState {
	return cb.cb.State()
}
