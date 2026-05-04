package engine

import (
	"errors"
	"log/slog"
	"time"

	kitbreaker "github.com/anatolykoptev/go-kit/breaker"
)

// CBState represents the circuit breaker state.
type CBState = kitbreaker.State

const (
	CBClosed   = kitbreaker.StateClosed
	CBOpen     = kitbreaker.StateOpen
	CBHalfOpen = kitbreaker.StateHalfOpen
)

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreaker wraps go-kit/breaker.Breaker with the dozor-flavored API
// (separate RecordSuccess/RecordFailure). All call sites in
// internal/mcpclient and pkg/extensions/mcpclient remain unchanged.
type CircuitBreaker struct {
	name string
	cb   *kitbreaker.Breaker
}

// NewCircuitBreaker creates a circuit breaker with the given name, failure
// threshold, and reset timeout (delay before transitioning to half-open).
//
// go-kit/breaker has no half-open hook, so the prior "circuit breaker
// half-open" log line is replaced by structured logs on trip/recover only
// (closed→open and half-open→closed transitions).
func NewCircuitBreaker(name string, threshold int, resetTimeout time.Duration) *CircuitBreaker {
	if threshold < 1 {
		threshold = 1
	}
	cb := kitbreaker.New(kitbreaker.Options{
		Name:          name,
		FailThreshold: uint32(threshold), //nolint:gosec // threshold validated >=1 above
		OpenDuration:  resetTimeout,
		OnTrip: func(n string) {
			slog.Info("circuit breaker opened", slog.String("target", n))
		},
		OnRecover: func(n string) {
			slog.Info("circuit breaker closed", slog.String("target", n))
		},
	})
	return &CircuitBreaker{name: name, cb: cb}
}

// Allow checks if a call should proceed. Acquires a permit that is released
// on RecordSuccess or RecordFailure.
func (cb *CircuitBreaker) Allow() bool {
	return cb.cb.Allow()
}

// RecordSuccess records a successful call.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.cb.Record(true)
}

// RecordFailure records a failed call.
func (cb *CircuitBreaker) RecordFailure() {
	cb.cb.Record(false)
}

// Execute runs fn if the circuit allows it, recording success/failure.
// Returns ErrCircuitOpen if the circuit is open.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if !cb.Allow() {
		return ErrCircuitOpen
	}
	err := fn()
	cb.cb.Record(err == nil)
	return err
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CBState {
	return cb.cb.State()
}
