// Package hedge provides hedged request execution.
// It starts a primary call and, if it hasn't returned after a delay,
// launches a secondary (hedge) call. The first successful result wins.
package hedge

import (
	"context"
	"time"
)

type result[T any] struct {
	val T
	err error
}

// Do executes fn and, if it hasn't returned successfully after delay,
// launches fn again in parallel. The first success wins; if both fail,
// the primary error is returned.
//
// If delay <= 0, fn is called once directly without goroutines.
//
// Use Do when retrying the SAME backend reduces tail latency (a flaky
// instance might respond on the second try). For PRIMARY→FALLBACK
// chains where the second call is a different endpoint/key, use
// DoFallback instead.
func Do[T any](ctx context.Context, delay time.Duration, fn func(context.Context) (T, error)) (T, error) {
	return doHedge(ctx, delay, fn, fn, false)
}

// DoFallback executes primary; if it hasn't returned successfully after
// delay, launches fallback in parallel. The first success wins. If
// primary returns an ERROR before the delay, fallback starts immediately
// (no waiting) — that's the point of having a fallback.
//
// If both fail, the primary error is returned (it's the canonical one;
// fallback errors are usually not what callers want to see).
//
// If delay <= 0, primary is called once directly without goroutines —
// fallback is unused. Pass any positive delay to enable fallback.
//
// Compared to Do(ctx, delay, fn), DoFallback differs in two ways:
//
//  1. The two calls invoke DIFFERENT functions — typical use is a
//     primary+fallback LLM provider chain, an alternate read replica,
//     or a multi-region failover.
//  2. A primary error before the delay triggers fallback IMMEDIATELY
//     (Do would wait for the delay even on early failure, since
//     re-running the same flaky fn faster doesn't help).
func DoFallback[T any](ctx context.Context, delay time.Duration, primary, fallback func(context.Context) (T, error)) (T, error) {
	return doHedge(ctx, delay, primary, fallback, true)
}

// doHedge is the shared core. earlyFallbackOnPrimaryErr distinguishes
// Do (false: same fn, retry would face the same flake) from DoFallback
// (true: different fn, jump to it as soon as primary fails).
func doHedge[T any](
	ctx context.Context,
	delay time.Duration,
	primary, secondary func(context.Context) (T, error),
	earlyFallbackOnPrimaryErr bool,
) (T, error) {
	if delay <= 0 {
		return primary(ctx)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan result[T], 2)

	go func() {
		v, err := primary(ctx)
		ch <- result[T]{v, err}
	}()

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case r := <-ch:
		// Primary returned before delay.
		if r.err == nil {
			return r.val, nil
		}
		// Primary failed early. For Do (same fn) we surface the error
		// immediately; for DoFallback we launch the secondary right now.
		if !earlyFallbackOnPrimaryErr {
			return r.val, r.err
		}
		// Track the primary error and start the secondary.
		go func() {
			v, err := secondary(ctx)
			ch <- result[T]{v, err}
		}()
		// Wait for secondary; if it succeeds, return its value; if it
		// fails, return the primary error (more diagnostic).
		select {
		case s := <-ch:
			if s.err == nil {
				return s.val, nil
			}
			return r.val, r.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	case <-timer.C:
		go func() {
			v, err := secondary(ctx)
			ch <- result[T]{v, err}
		}()
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}

	// Two goroutines running — collect up to 2 results.
	var firstErr error
	for range 2 {
		select {
		case r := <-ch:
			if r.err == nil {
				return r.val, nil
			}
			if firstErr == nil {
				firstErr = r.err
			}
		case <-ctx.Done():
			var zero T
			if firstErr != nil {
				return zero, firstErr
			}
			return zero, ctx.Err()
		}
	}

	var zero T
	return zero, firstErr
}
