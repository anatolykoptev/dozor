package breaker

import (
	"math"
	"math/rand/v2"
	"time"
)

// computeBackoff returns the cooldown duration for the given number of consecutive
// trip cycles (1-indexed: first trip uses consecutiveOpens=1).
//
// Formula: base * multiplier^(consecutiveOpens-1), capped at maxOpenDuration,
// then ±jitterPct% random jitter applied symmetrically.
//
// Zero or negative BackoffMultiplier collapses to constant backoff = OpenDuration.
func computeBackoff(opts Options, consecutiveOpens int) time.Duration {
	base := opts.OpenDuration
	if consecutiveOpens < 1 {
		consecutiveOpens = 1
	}
	mult := opts.BackoffMultiplier
	if mult < 1.0 {
		mult = 1.0 // constant backoff
	}
	factor := math.Pow(mult, float64(consecutiveOpens-1))
	grown := time.Duration(float64(base) * factor)
	if opts.MaxOpenDuration > 0 && grown > opts.MaxOpenDuration {
		grown = opts.MaxOpenDuration
	}
	if grown <= 0 {
		grown = base
	}
	if opts.JitterPct <= 0 {
		return grown
	}
	range01 := (rand.Float64()*2 - 1) //nolint:gosec // not security-critical
	jitter := time.Duration(float64(grown) * float64(opts.JitterPct) / 100.0 * range01)
	result := grown + jitter
	if result <= 0 {
		return base
	}
	return result
}
