// Package ratelimit provides token bucket rate limiters.
// Limiter is a single rate limiter; KeyLimiter manages per-key limiters.
// Zero external dependencies.
package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrLimiterZero is returned by Wait when the limiter can never produce tokens
// (rate and burst are both zero, or burst is zero).
var ErrLimiterZero = errors.New("ratelimit: zero rate and insufficient burst — token can never be available")

// Limiter is a token bucket rate limiter.
type Limiter struct {
	mu         sync.Mutex
	tokens     float64
	rate       float64   // tokens per second
	burst      float64   // max capacity
	lastRefill time.Time
	now        func() time.Time // test override
}

// New creates a Limiter that generates tokens at rate per second
// with a maximum burst capacity.
func New(rate float64, burst int) *Limiter {
	return &Limiter{
		tokens:     float64(burst),
		rate:       rate,
		burst:      float64(burst),
		lastRefill: time.Now(),
		now:        time.Now,
	}
}

func (l *Limiter) refill() {
	now := l.now()
	elapsed := now.Sub(l.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tokens += elapsed * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.lastRefill = now
}

// Allow reports whether one token is available and consumes it.
// Returns immediately without blocking.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

// Wait blocks until a token is available or ctx is cancelled.
// Returns ErrLimiterZero if the limiter can never produce a token (rate=0 with no burst remaining, or burst=0).
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		l.refill()
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		// If rate is zero, tokens will never increase — fail fast.
		if l.rate <= 0 {
			l.mu.Unlock()
			return ErrLimiterZero
		}
		// If burst < 1, refill caps below 1 — token can never be consumed.
		if l.burst < 1 {
			l.mu.Unlock()
			return ErrLimiterZero
		}
		// Compute wait time for one token.
		deficit := 1.0 - l.tokens
		wait := time.Duration(deficit / l.rate * float64(time.Second))
		l.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
