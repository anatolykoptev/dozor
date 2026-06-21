package engine

import (
	"sync"
	"testing"
	"time"
)

func makeRingAlert(svc string, level AlertLevel, offset time.Duration) Alert {
	return Alert{
		Level:           level,
		Service:         svc,
		Title:           svc + " alert",
		Description:     "desc",
		SuggestedAction: "check " + svc,
		Timestamp:       time.Now().Add(offset),
	}
}

func TestAlertRing_Empty(t *testing.T) {
	r := NewAlertRing(3)
	got := r.Recent(time.Hour, 10)
	if len(got) != 0 {
		t.Fatalf("expected 0 records from empty ring, got %d", len(got))
	}
}

func TestAlertRing_SingleAdd(t *testing.T) {
	r := NewAlertRing(3)
	r.Add(makeRingAlert("svc-a", AlertCritical, 0))

	got := r.Recent(time.Hour, 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].Service != "svc-a" {
		t.Errorf("expected svc-a, got %s", got[0].Service)
	}
}

func TestAlertRing_Wraparound(t *testing.T) {
	// Capacity 3; add 5 — oldest 2 must be evicted.
	r := NewAlertRing(3)
	for i := range 5 {
		r.Add(makeRingAlert("svc", AlertWarning, time.Duration(i)*time.Millisecond))
	}

	// Ring should contain exactly 3 entries.
	got := r.Recent(0, 0) // no time filter, default limit
	if len(got) != 3 {
		t.Fatalf("expected 3 entries after wraparound, got %d", len(got))
	}
}

// TestAlertRing_WrapRetainsNewest verifies the OLDEST entries are overwritten,
// not the newest. We use distinct titles to identify which survived.
func TestAlertRing_WrapRetainsNewest(t *testing.T) {
	r := NewAlertRing(3)
	// Add in order oldest → newest so timestamps increase.
	titles := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	base := time.Now().Add(-10 * time.Second)
	for i, title := range titles {
		a := makeRingAlert("svc", AlertWarning, 0)
		a.Title = title
		a.Timestamp = base.Add(time.Duration(i) * time.Second)
		r.Add(a)
	}

	// After 5 adds into capacity-3 ring: gamma, delta, epsilon survive.
	got := r.Recent(0, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// Recent returns newest-first: epsilon, delta, gamma.
	want := []string{"epsilon", "delta", "gamma"}
	for i, rec := range got {
		if rec.Title != want[i] {
			t.Errorf("idx %d: want title %q, got %q", i, want[i], rec.Title)
		}
	}
}

func TestAlertRing_TimeFilter(t *testing.T) {
	r := NewAlertRing(10)
	now := time.Now()

	// The time filter keys on DELIVERY time (addAt), not the alert's own
	// Timestamp — so inject with explicit ingestion times.
	r.addAt(makeRingAlert("old-svc", AlertWarning, 0), now.Add(-3*time.Hour))
	r.addAt(makeRingAlert("new-svc", AlertCritical, 0), now.Add(-10*time.Minute))

	// 1h filter: only new-svc should appear.
	got := r.Recent(time.Hour, 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 after 1h filter, got %d", len(got))
	}
	if got[0].Service != "new-svc" {
		t.Errorf("expected new-svc, got %s", got[0].Service)
	}
}

// TestAlertRing_AddStampsIngestionTime is the regression guard for the empirically
// found bug: an alert whose own Timestamp is zero/stale (e.g. an Alertmanager alert
// delivered without startsAt, or a long-firing alert with an old StartsAt) must still
// be visible in a since-window, because Add stamps the delivery time.
func TestAlertRing_AddStampsIngestionTime(t *testing.T) {
	r := NewAlertRing(5)

	// Alert with a ZERO own-Timestamp (the probe-1 failure mode).
	a := makeRingAlert("zero-ts-svc", AlertWarning, 0)
	a.Timestamp = time.Time{}
	r.Add(a)

	// A long-firing alert whose StartsAt was 6h ago, delivered now.
	stale := makeRingAlert("stale-start-svc", AlertCritical, 0)
	stale.Timestamp = time.Now().Add(-6 * time.Hour)
	r.Add(stale)

	// Both were just delivered, so a 1h window must surface both.
	got := r.Recent(time.Hour, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 (both stamped at delivery), got %d", len(got))
	}
	for _, rec := range got {
		if rec.Timestamp.IsZero() {
			t.Errorf("record %q kept a zero timestamp; Add must stamp ingestion time", rec.Service)
		}
	}
}

func TestAlertRing_NewestFirst(t *testing.T) {
	r := NewAlertRing(5)
	base := time.Now().Add(-5 * time.Minute)
	services := []string{"a", "b", "c"}
	for i, svc := range services {
		a := makeRingAlert(svc, AlertWarning, 0)
		a.Timestamp = base.Add(time.Duration(i) * time.Minute)
		r.Add(a)
	}

	got := r.Recent(time.Hour, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// Newest first: c, b, a.
	wantOrder := []string{"c", "b", "a"}
	for i, rec := range got {
		if rec.Service != wantOrder[i] {
			t.Errorf("idx %d: want %s, got %s", i, wantOrder[i], rec.Service)
		}
	}
}

func TestAlertRing_LimitCap(t *testing.T) {
	r := NewAlertRing(20)
	for range 15 {
		r.Add(makeRingAlert("svc", AlertInfo, 0))
	}

	got := r.Recent(0, 5)
	if len(got) != 5 {
		t.Fatalf("expected limit of 5, got %d", len(got))
	}
}

func TestAlertRing_DefaultLimit(t *testing.T) {
	// limit<=0 should use defaultRecentLimit (50).
	r := NewAlertRing(200)
	for range 100 {
		r.Add(makeRingAlert("svc", AlertInfo, 0))
	}
	got := r.Recent(0, 0)
	if len(got) != defaultRecentLimit {
		t.Fatalf("expected %d (default limit), got %d", defaultRecentLimit, len(got))
	}
}

func TestAlertRing_DefaultCapacity(t *testing.T) {
	r := NewAlertRing(0)
	if r.capacity != defaultRingCapacity {
		t.Errorf("expected capacity %d, got %d", defaultRingCapacity, r.capacity)
	}
}

// TestAlertRing_ConcurrentAdd verifies no data race under concurrent writes.
// Run with: go test -race ./internal/engine/
func TestAlertRing_ConcurrentAdd(t *testing.T) {
	r := NewAlertRing(10)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Add(makeRingAlert("svc", AlertWarning, 0))
			_ = r.Recent(time.Hour, 10)
		}(i)
	}
	wg.Wait()
	// If we reach here without panic/race the test passes.
}
