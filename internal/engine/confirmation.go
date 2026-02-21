package engine

import (
	"log/slog"
	"sync"
	"time"
)

// FailureTracker requires N consecutive failures before confirming an alert.
// A single success resets the counter. Thread-safe.
type FailureTracker struct {
	mu        sync.Mutex
	counts    map[string]int
	firstSeen map[string]time.Time
	threshold int
}

// NewFailureTracker creates a tracker that requires threshold consecutive failures.
func NewFailureTracker(threshold int) *FailureTracker {
	if threshold < 1 {
		threshold = 1
	}
	return &FailureTracker{
		counts:    make(map[string]int),
		firstSeen: make(map[string]time.Time),
		threshold: threshold,
	}
}

// RecordFailure increments the consecutive failure count for key.
// Returns (confirmed, count) where confirmed is true when count >= threshold.
func (ft *FailureTracker) RecordFailure(key string) (confirmed bool, count int) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if _, ok := ft.firstSeen[key]; !ok {
		ft.firstSeen[key] = time.Now()
	}
	ft.counts[key]++
	count = ft.counts[key]
	confirmed = count >= ft.threshold
	return
}

// RecordSuccess resets the counter for key.
func (ft *FailureTracker) RecordSuccess(key string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	delete(ft.counts, key)
	delete(ft.firstSeen, key)
}

// FlapStatus is the result of a flap detection check.
type FlapStatus struct {
	Flapping   bool
	ChangeRate float64
}

// FlapDetector tracks state oscillation and suppresses flapping services.
// Uses a sliding window of boolean results and calculates state-change percentage.
type FlapDetector struct {
	mu       sync.Mutex
	history  map[string][]bool
	size     int
	highPct  float64 // start suppressing above this
	lowPct   float64 // stop suppressing below this
	flapping map[string]bool
}

// NewFlapDetector creates a detector with the given window size and thresholds.
func NewFlapDetector(windowSize int, highPct, lowPct float64) *FlapDetector {
	if windowSize < 3 {
		windowSize = 3
	}
	return &FlapDetector{
		history:  make(map[string][]bool),
		size:     windowSize,
		highPct:  highPct,
		lowPct:   lowPct,
		flapping: make(map[string]bool),
	}
}

// Record adds a result (ok=true for success, false for failure) and returns
// the current flap status for this key.
func (fd *FlapDetector) Record(key string, ok bool) FlapStatus {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	h := fd.history[key]
	h = append(h, ok)
	if len(h) > fd.size {
		h = h[len(h)-fd.size:]
	}
	fd.history[key] = h

	// Need at least 3 samples to detect flapping.
	if len(h) < 3 {
		return FlapStatus{Flapping: fd.flapping[key]}
	}

	// Calculate state-change rate: number of transitions / (len-1).
	changes := 0
	for i := 1; i < len(h); i++ {
		if h[i] != h[i-1] {
			changes++
		}
	}
	rate := float64(changes) / float64(len(h)-1)

	wasFlapping := fd.flapping[key]
	if !wasFlapping && rate >= fd.highPct {
		fd.flapping[key] = true
		slog.Info("flap detection: service started flapping",
			slog.String("service", key),
			slog.Float64("change_rate", rate))
	} else if wasFlapping && rate <= fd.lowPct {
		fd.flapping[key] = false
		slog.Info("flap detection: service stopped flapping",
			slog.String("service", key),
			slog.Float64("change_rate", rate))
	}

	return FlapStatus{
		Flapping:   fd.flapping[key],
		ChangeRate: rate,
	}
}

// AlertKey returns a dedup key for an alert, matching hashAlerts format.
func AlertKey(a Alert) string {
	return string(a.Level) + ":" + a.Service + ":" + a.Title
}
