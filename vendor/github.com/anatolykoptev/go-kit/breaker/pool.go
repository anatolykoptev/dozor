// breaker/pool.go
package breaker

import "sync"

// Pool is a lazy per-key registry of Breakers. Options for each breaker are
// produced on first Get(key) by the provided factory.
type Pool struct {
	mu      sync.Mutex
	factory func(key string) Options
	m       map[string]*Breaker
}

func NewPool(factory func(key string) Options) *Pool {
	return &Pool{factory: factory, m: make(map[string]*Breaker)}
}

// Get returns the breaker for key, creating it on first access.
func (p *Pool) Get(key string) *Breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.m[key]; ok {
		return b
	}
	b := New(p.factory(key))
	p.m[key] = b
	return b
}

// Snapshot returns a shallow copy of the current pool for metrics/display.
func (p *Pool) Snapshot() map[string]*Breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*Breaker, len(p.m))
	for k, v := range p.m {
		out[k] = v
	}
	return out
}
