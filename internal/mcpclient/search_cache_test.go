package mcpclient

import (
	"testing"
	"time"
)

func TestSearchCache_MissThenHit(t *testing.T) {
	c := newSearchCache()
	k := cacheKey("postgres deadlock", "devops", "devops", 5)
	if v, ok := c.get(k); ok {
		t.Fatalf("expected miss on empty cache, got %q", v)
	}
	c.set(k, "result body")
	v, ok := c.get(k)
	if !ok || v != "result body" {
		t.Errorf("expected hit with 'result body', got %q / %v", v, ok)
	}
}

func TestSearchCache_DifferentKeysIsolated(t *testing.T) {
	c := newSearchCache()
	k1 := cacheKey("q1", "devops", "devops", 5)
	k2 := cacheKey("q2", "devops", "devops", 5)
	c.set(k1, "a")
	c.set(k2, "b")
	if v, _ := c.get(k1); v != "a" {
		t.Errorf("k1: expected 'a', got %q", v)
	}
	if v, _ := c.get(k2); v != "b" {
		t.Errorf("k2: expected 'b', got %q", v)
	}
}

func TestSearchCache_CacheKeyDeterministic(t *testing.T) {
	k1 := cacheKey("q", "u", "c", 5)
	k2 := cacheKey("q", "u", "c", 5)
	if k1 != k2 {
		t.Errorf("cacheKey should be deterministic, got %q vs %q", k1, k2)
	}
	k3 := cacheKey("q", "u", "c", 10)
	if k1 == k3 {
		t.Error("cacheKey should differ when topK changes")
	}
}

func TestSearchCache_Expiry(t *testing.T) {
	c := newSearchCache()
	k := cacheKey("q", "u", "c", 5)
	c.entries[k] = searchCacheEntry{
		value:   "stale",
		expires: time.Now().Add(-time.Second),
	}
	c.order = append(c.order, k)
	if _, ok := c.get(k); ok {
		t.Error("expected expired entry to be a miss")
	}
	if c.Len() != 0 {
		t.Errorf("expected cache to be empty after expired read, got %d", c.Len())
	}
}

// TestSearchCache_OrderSyncAfterExpiry is a regression guard for a bug
// where expired entries were removed from the entries map but left in the
// order slice. After sweeps via get(), subsequent set() calls could
// exceed capacity because the eviction loop popped stale keys from order
// that were already gone from entries — the delete was a no-op and
// len(entries) never decremented below the cap.
func TestSearchCache_OrderSyncAfterExpiry(t *testing.T) {
	c := newSearchCache()

	for i := 0; i < 10; i++ {
		k := cacheKey("q", "u", "c", i)
		c.entries[k] = searchCacheEntry{
			value:   "stale",
			expires: time.Now().Add(-time.Second),
		}
		c.order = append(c.order, k)
	}
	for i := 0; i < 10; i++ {
		k := cacheKey("q", "u", "c", i)
		if _, ok := c.get(k); ok {
			t.Errorf("entry %d should have been expired", i)
		}
	}
	if c.Len() != 0 {
		t.Errorf("entries should be empty after sweep, got %d", c.Len())
	}
	if len(c.order) != 0 {
		t.Errorf("order should be empty after sweep, got %d", len(c.order))
	}
}

func TestSearchCache_FIFOEviction(t *testing.T) {
	c := newSearchCache()
	for i := 0; i < searchCacheMaxEntries+10; i++ {
		k := cacheKey("q", "u", "c", i)
		c.set(k, "v")
	}
	if c.Len() > searchCacheMaxEntries {
		t.Errorf("cache exceeded capacity: %d > %d", c.Len(), searchCacheMaxEntries)
	}
	if _, ok := c.get(cacheKey("q", "u", "c", 0)); ok {
		t.Error("expected oldest entry to be evicted")
	}
	if _, ok := c.get(cacheKey("q", "u", "c", searchCacheMaxEntries+9)); !ok {
		t.Error("expected newest entry to be cached")
	}
}
