package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// searchCacheTTL is the lifetime of a cached memdb_search result. Long enough
// to de-duplicate repeated queries in a single investigation (the agent
// typically asks the same question 2-3 times in quick succession while
// reasoning), short enough that a human rerunning a query after several
// minutes sees fresh results.
const searchCacheTTL = 60 * time.Second

// searchCacheMaxEntries caps cache memory at a few hundred entries. At 1 KB
// average result size that's well under a megabyte.
const searchCacheMaxEntries = 256

// searchCacheEntry holds a cached response plus its expiry timestamp.
type searchCacheEntry struct {
	value   string
	expires time.Time
}

// searchCache is a simple TTL + FIFO-eviction map keyed by a hash of the
// query parameters. Concurrency-safe for the workloads dozor sees — a single
// agent loop plus occasional watch ticks.
type searchCache struct {
	mu      sync.Mutex
	entries map[string]searchCacheEntry
	// order preserves insertion order for FIFO eviction when the map hits
	// searchCacheMaxEntries. We do not bother with LRU — the TTL does most
	// of the eviction work and memdb_search results are cheap to recompute.
	order []string
}

// newSearchCache allocates an empty cache.
func newSearchCache() *searchCache {
	return &searchCache{
		entries: make(map[string]searchCacheEntry, searchCacheMaxEntries),
	}
}

// cacheKey is a deterministic hash of the search parameters. Using a hash
// instead of a concatenation keeps memory bounded regardless of query size.
func cacheKey(query, userID, cubeID string, topK int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%d", query, userID, cubeID, topK)
	return hex.EncodeToString(h.Sum(nil))
}

// get returns the cached value for key plus true if present and not expired.
// On expiry, the entry is removed from BOTH the entries map AND the order
// slice so the FIFO eviction queue stays in sync. Otherwise stale keys
// would accumulate in order and subsequent set() calls could exceed
// capacity before eviction catches up.
func (c *searchCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expires) {
		delete(c.entries, key)
		c.removeFromOrder(key)
		return "", false
	}
	return entry.value, true
}

// removeFromOrder strips key from the order slice. Linear scan; the slice
// is bounded by searchCacheMaxEntries so this is O(256) worst case.
// Caller must hold c.mu.
func (c *searchCache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// set stores value under key with the configured TTL, evicting the oldest
// entry if the cache is at capacity. When the head of the order queue has
// already been expired out of the entries map (from a prior get), the
// eviction loop skips it and pops the next key until it finds a live one
// — this keeps entries and order in sync even under mixed get/set traffic.
func (c *searchCache) set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, existing := c.entries[key]; !existing {
		for len(c.entries) >= searchCacheMaxEntries && len(c.order) > 0 {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, key)
	}
	c.entries[key] = searchCacheEntry{
		value:   value,
		expires: time.Now().Add(searchCacheTTL),
	}
}

// Len reports the current number of live entries (including expired ones
// that have not yet been swept by get). Used by tests.
func (c *searchCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
