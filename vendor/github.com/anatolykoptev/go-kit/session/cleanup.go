package session

import "time"

// Cleanup removes stale sessions from the store. Sessions older than maxAge
// are archived (if archiveFn is provided) and then deleted.
// Returns the number of deleted sessions. If maxAge <= 0, cleanup is disabled.
func Cleanup(store Store, maxAge time.Duration, archiveFn func(*Session) error) int {
	if maxAge <= 0 {
		return 0
	}

	staleKeys := store.ListStale(maxAge)
	count := 0

	for _, key := range staleKeys {
		if archiveFn != nil {
			_ = archiveFn(store.GetOrCreate(key))
		}
		_ = store.Delete(key)
		count++
	}

	return count
}
