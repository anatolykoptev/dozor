package ratelimit

import (
	"context"
	"sync"
	"time"
)

type keyEntry struct {
	limiter    *Limiter
	lastAccess time.Time
}

// KeyLimiter manages per-key rate limiters with automatic cleanup of idle keys.
type KeyLimiter struct {
	mu       sync.Mutex
	limiters map[string]*keyEntry
	rate     float64
	burst    int
	done     chan struct{}
	now      func() time.Time
}

// NewKeyLimiter creates a KeyLimiter where each key gets its own
// token bucket with the given rate and burst.
func NewKeyLimiter(rate float64, burst int) *KeyLimiter {
	return &KeyLimiter{
		limiters: make(map[string]*keyEntry),
		rate:     rate,
		burst:    burst,
		done:     make(chan struct{}),
		now:      time.Now,
	}
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
func (kl *KeyLimiter) StartCleanup(interval, maxIdle time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-kl.done:
				return
			case <-ticker.C:
				kl.Cleanup(maxIdle)
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

// Close stops background cleanup.
func (kl *KeyLimiter) Close() {
	select {
	case <-kl.done:
	default:
		close(kl.done)
	}
}
