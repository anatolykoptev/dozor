package breaker

import (
	"errors"
	"time"
)

// State is the current breaker state.
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half-open"
)

// ErrOpen is returned by Execute and HTTPDoer when the breaker is open and the
// call was fast-failed without reaching the wrapped target.
var ErrOpen = errors.New("breaker: open")

// Options configures a Breaker. Zero values are filled with sensible defaults.
type Options struct {
	Name string // for logs/metrics (default "breaker")

	// Count-based trip: opens after this many consecutive failures.
	// Must be >=1. Default 5.
	FailThreshold uint32

	// Rate-based trip: RecordCycle opens when failed/total >= FailRateThreshold.
	// 0 disables rate-based tripping. Range (0, 1].
	FailRateThreshold float64

	// OpenDuration is the base cooldown. Must be >0.
	// Default 60s.
	OpenDuration time.Duration

	// BackoffMultiplier multiplies OpenDuration on each consecutive trip.
	// 1.0 = constant; 2.0 = doubling. Default 1.0.
	BackoffMultiplier float64

	// MaxOpenDuration caps the grown cooldown. 0 disables the cap.
	MaxOpenDuration time.Duration

	// JitterPct applies ±JitterPct% random jitter to the computed cooldown.
	// 0-100. Default 0 (no jitter).
	JitterPct int

	// MaxHalfOpenCalls is the number of probe calls allowed in half-open state.
	// Must be >=1. Default 1 (classic probe slot).
	MaxHalfOpenCalls uint32

	// Hooks fired on state transitions. Called in new goroutine — must not block.
	OnTrip    func(name string)
	OnRecover func(name string)
}

func applyDefaults(o *Options) {
	if o.Name == "" {
		o.Name = "breaker"
	}
	if o.FailThreshold == 0 {
		o.FailThreshold = 5
	}
	if o.OpenDuration <= 0 {
		o.OpenDuration = 60 * time.Second
	}
	if o.BackoffMultiplier < 1.0 {
		o.BackoffMultiplier = 1.0
	}
	if o.MaxHalfOpenCalls == 0 {
		o.MaxHalfOpenCalls = 1
	}
}
