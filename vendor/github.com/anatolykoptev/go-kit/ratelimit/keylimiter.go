package ratelimit

import (
	"context"
	"runtime"
	"sync"
	"time"
	"weak"
)

type keyEntry struct {
	limiter    *Limiter
	lastAccess time.Time
}

// KeyLimiter manages per-key rate limiters with automatic cleanup of idle keys.
//
// Call Close() when finished to stop any background cleanup goroutine started
// via StartCleanup or the WithAutoCleanup option. As a safety net, NewKeyLimiter
// registers a runtime finalizer that calls Close() when the KeyLimiter is
// garbage-collected, preventing goroutine leaks when Close() is never called.
// Explicit Close() is strongly preferred — finalizers are not guaranteed to run
// promptly.
type KeyLimiter struct {
	mu       sync.Mutex
	limiters map[string]*keyEntry
	rate     float64
	burst    int
	done     chan struct{}
	now      func() time.Time
}

// Option configures a KeyLimiter created by NewKeyLimiter.
type Option func(*KeyLimiter)

// WithAutoCleanup starts a background cleanup goroutine that reaps limiters
// idle longer than maxIdle, every interval. This relieves callers from having
// to call StartCleanup manually and bounds map growth for workloads with a
// high cardinality of transient keys. The goroutine is stopped by Close()
// (or by the runtime finalizer registered in NewKeyLimiter).
func WithAutoCleanup(interval, maxIdle time.Duration) Option {
	return func(kl *KeyLimiter) {
		kl.startCleanup(interval, maxIdle)
	}
}

// NewKeyLimiter creates a KeyLimiter where each key gets its own
// token bucket with the given rate and burst. Optional opts may be supplied,
// e.g. WithAutoCleanup to start background reaping of idle keys automatically.
func NewKeyLimiter(rate float64, burst int, opts ...Option) *KeyLimiter {
	kl := &KeyLimiter{
		limiters: make(map[string]*keyEntry),
		rate:     rate,
		burst:    burst,
		done:     make(chan struct{}),
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(kl)
	}
	// Register a finalizer so that if the caller forgets Close(), any
	// background cleanup goroutine is still stopped when the KeyLimiter is
	// garbage-collected. This prevents goroutine leaks in long-running
	// services that create transient KeyLimiters (per-request / per-tenant).
	// Close() clears the finalizer to avoid a double-close.
	runtime.SetFinalizer(kl, func(kl *KeyLimiter) { kl.Close() })
	return kl
}

func (kl *KeyLimiter) getOrCreate(key string) *Limiter {
	kl.mu.Lock()
	defer kl.mu.Unlock()
	ke, ok := kl.limiters[key]
	if ok {
		ke.lastAccess = kl.now()
		return ke.limiter
	}
	l := New(kl.rate, kl.burst)
	l.now = kl.now
	kl.limiters[key] = &keyEntry{limiter: l, lastAccess: kl.now()}
	return l
}

// Allow reports whether one token is available for key.
func (kl *KeyLimiter) Allow(key string) bool {
	return kl.getOrCreate(key).Allow()
}

// Wait blocks until a token is available for key or ctx is cancelled.
func (kl *KeyLimiter) Wait(ctx context.Context, key string) error {
	return kl.getOrCreate(key).Wait(ctx)
}

// Cleanup removes limiters idle longer than maxIdle. Returns count removed.
func (kl *KeyLimiter) Cleanup(maxIdle time.Duration) int {
	kl.mu.Lock()
	defer kl.mu.Unlock()
	now := kl.now()
	removed := 0
	for key, ke := range kl.limiters {
		if now.Sub(ke.lastAccess) > maxIdle {
			delete(kl.limiters, key)
			removed++
		}
	}
	return removed
}

// StartCleanup runs periodic cleanup in a background goroutine.
// Call Close() to stop.
//
// The goroutine holds only the done channel (a separate heap object) and a
// weak pointer to the KeyLimiter, so it does NOT prevent the KeyLimiter from
// being garbage-collected. This allows the finalizer registered in
// NewKeyLimiter to fire even if Close() is never called, breaking the
// reference cycle that would otherwise keep the KeyLimiter (and its goroutine)
// alive forever.
func (kl *KeyLimiter) StartCleanup(interval, maxIdle time.Duration) {
	kl.startCleanup(interval, maxIdle)
}

// startCleanup is the shared implementation used by StartCleanup and the
// WithAutoCleanup option. It is kept separate so that the goroutine launch
// site is identical regardless of how cleanup is initiated.
func (kl *KeyLimiter) startCleanup(interval, maxIdle time.Duration) {
	done := kl.done
	wp := weak.Make(kl)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if cc := wp.Value(); cc != nil {
					cc.Cleanup(maxIdle)
				} else {
					// KeyLimiter was garbage-collected; exit to avoid a leak.
					return
				}
			}
		}
	}()
}

// Len returns the number of active key limiters.
func (kl *KeyLimiter) Len() int {
	kl.mu.Lock()
	defer kl.mu.Unlock()
	return len(kl.limiters)
}

// Close stops background cleanup. It is safe to call multiple times.
//
//go:noinline
func (kl *KeyLimiter) Close() {
	// Clear the finalizer first so a GC-triggered Close cannot double-close.
	runtime.SetFinalizer(kl, nil)

	select {
	case <-kl.done:
	default:
		close(kl.done)
	}
}
