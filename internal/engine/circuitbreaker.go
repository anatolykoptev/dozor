package engine

import (
	"errors"
	"log/slog"
	"sync"
	"time"
)

// CBState represents the circuit breaker state.
type CBState int

const (
	CBClosed   CBState = iota // normal operation
	CBOpen                    // failing, reject calls
	CBHalfOpen                // probing with a single call
)

func (s CBState) String() string {
	switch s {
	case CBClosed:
		return "closed"
	case CBOpen:
		return "open"
	case CBHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreaker protects against cascading failures from external dependencies.
// State machine: Closed -> Open -> HalfOpen -> Closed (on success) or Open (on failure).
type CircuitBreaker struct {
	mu           sync.Mutex
	name         string
	state        CBState
	failures     int
	lastFailure  time.Time
	threshold    int
	resetTimeout time.Duration
}

// NewCircuitBreaker creates a circuit breaker with the given name, failure threshold,
// and reset timeout (time to wait before probing in half-open state).
func NewCircuitBreaker(name string, threshold int, resetTimeout time.Duration) *CircuitBreaker {
	if threshold < 1 {
		threshold = 1
	}
	return &CircuitBreaker{
		name:         name,
		state:        CBClosed,
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Allow checks if a call should proceed.
// Returns true if the circuit is closed or has transitioned to half-open.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CBClosed:
		return true
	case CBOpen:
		if time.Since(cb.lastFailure) >= cb.resetTimeout {
			cb.state = CBHalfOpen
			slog.Info("circuit breaker half-open, probing",
				slog.String("target", cb.name))
			return true
		}
		return false
	case CBHalfOpen:
		// Only one probe at a time; reject concurrent calls while probing.
		return false
	default:
		return true
	}
}

// RecordSuccess records a successful call. Resets the breaker to closed.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == CBHalfOpen {
		slog.Info("circuit breaker closed after successful probe",
			slog.String("target", cb.name))
	}
	cb.state = CBClosed
	cb.failures = 0
}

// RecordFailure records a failed call. Opens the breaker if threshold is reached.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	if cb.state == CBHalfOpen {
		cb.state = CBOpen
		slog.Info("circuit breaker re-opened after probe failure",
			slog.String("target", cb.name),
			slog.Int("failures", cb.failures))
		return
	}

	if cb.failures >= cb.threshold {
		cb.state = CBOpen
		slog.Info("circuit breaker opened",
			slog.String("target", cb.name),
			slog.Int("failures", cb.failures),
			slog.Int("threshold", cb.threshold))
	}
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CBState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
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
