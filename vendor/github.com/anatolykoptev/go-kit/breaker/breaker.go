// Package breaker provides a 3-state circuit breaker (closed/open/half-open) with
// exponential cooldown, optional jitter, and probe-slot semantics.
//
// # Usage contract
//
// Every successful Allow() MUST be paired with exactly one Record(success bool).
// Skipping Record after a half-open probe wedges the breaker permanently — the
// probe slot is never released. If you cannot guarantee pairing (e.g. panics in
// user code), prefer the higher-level Execute[T] or HTTPDoer wrappers, which
// handle pairing automatically.
//
// # Modes
//
//   - Count-based: Record(true/false). Trips after FailThreshold consecutive
//     failures.
//   - Rate-based: RecordCycle(total, failed). Trips when failed/total exceeds
//     FailRateThreshold. Requires FailRateThreshold > 0.
//
// Do not mix Record and RecordCycle on the same breaker instance.
package breaker

import (
	"sync"
	"time"
)

// Breaker is a thread-safe circuit breaker.
type Breaker struct {
	opts Options

	mu               sync.Mutex
	state            State
	consecutiveFails uint32 // reset on success or open
	consecutiveOpens int    // incremented on each trip; grows backoff
	openUntil        time.Time
	halfOpenInFlight uint32 // probe calls currently allowed but not yet recorded
}

// New returns a Breaker in closed state.
func New(opts Options) *Breaker {
	applyDefaults(&opts)
	return &Breaker{opts: opts, state: StateClosed}
}

// State returns the current breaker state (may auto-transition open→half-open).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeTransitionToHalfOpen()
	return b.state
}

// Allow reports whether a call may proceed. In half-open state, returns true only
// for the first MaxHalfOpenCalls calls; subsequent calls are blocked until one of
// the probes completes with Record.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeTransitionToHalfOpen()
	switch b.state {
	case StateClosed:
		return true
	case StateHalfOpen:
		if b.halfOpenInFlight >= b.opts.MaxHalfOpenCalls {
			return false
		}
		b.halfOpenInFlight++
		return true
	default: // StateOpen
		return false
	}
}

// Record reports the outcome of a call. Must be paired with Allow().
func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case StateClosed:
		if success {
			b.consecutiveFails = 0
			return
		}
		b.consecutiveFails++
		if b.consecutiveFails >= b.opts.FailThreshold {
			b.tripToOpen()
		}
	case StateHalfOpen:
		if b.halfOpenInFlight > 0 {
			b.halfOpenInFlight--
		}
		if success {
			b.reset()
		} else {
			b.tripToOpen()
		}
	case StateOpen:
		// Record while open is a no-op (caller should have checked Allow).
	}
}

// RecordCycle records aggregate outcomes from a scan cycle. Requires
// Options.FailRateThreshold to be > 0 — no-op otherwise (use Record for
// count-based trip). Ignored if total <= 0.
//
// Callers may invoke RecordCycle in any state; it auto-transitions open→half-open
// when the cooldown has elapsed, so a late Record after a long idle period stays
// accurate even if Allow() was not called in between.
func (b *Breaker) RecordCycle(total, failed int) {
	if total <= 0 || b.opts.FailRateThreshold <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeTransitionToHalfOpen()
	rate := float64(failed) / float64(total)
	tripped := rate >= b.opts.FailRateThreshold
	switch b.state {
	case StateClosed:
		if tripped {
			b.tripToOpen()
		}
	case StateHalfOpen:
		if b.halfOpenInFlight > 0 {
			b.halfOpenInFlight--
		}
		if tripped {
			b.tripToOpen()
		} else {
			b.reset()
		}
	case StateOpen:
		// No-op while open
	}
}

// Must be called with b.mu held.
func (b *Breaker) maybeTransitionToHalfOpen() {
	if b.state != StateOpen {
		return
	}
	if time.Now().Before(b.openUntil) {
		return
	}
	b.state = StateHalfOpen
	b.halfOpenInFlight = 0
}

// Must be called with b.mu held.
func (b *Breaker) tripToOpen() {
	b.consecutiveOpens++
	b.consecutiveFails = 0
	b.halfOpenInFlight = 0
	b.state = StateOpen
	b.openUntil = time.Now().Add(computeBackoff(b.opts, b.consecutiveOpens))
	if b.opts.OnTrip != nil {
		go b.opts.OnTrip(b.opts.Name)
	}
}

// ForceHalfOpen forces the breaker into half-open state regardless of its current
// state or remaining cooldown. Intended for admin/operator-triggered resets.
// The probe-slot counter is also reset so the next Allow() returns true.
func (b *Breaker) ForceHalfOpen() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = StateHalfOpen
	b.halfOpenInFlight = 0
}

// Must be called with b.mu held.
func (b *Breaker) reset() {
	b.state = StateClosed
	b.consecutiveFails = 0
	b.consecutiveOpens = 0
	b.halfOpenInFlight = 0
	if b.opts.OnRecover != nil {
		go b.opts.OnRecover(b.opts.Name)
	}
}
