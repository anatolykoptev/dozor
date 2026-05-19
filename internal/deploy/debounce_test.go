package deploy

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic timer tests.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		clock:  c,
		fireAt: c.now.Add(d),
		ch:     make(chan time.Time, 1),
		alive:  true,
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d and fires every timer whose deadline
// is now in the past.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	due := make([]*fakeTimer, 0, len(c.timers))
	for _, t := range c.timers {
		if t.alive && !t.fired && !now.Before(t.fireAt) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.ch <- t.fireAt
	}
}

type fakeTimer struct {
	clock  *fakeClock
	fireAt time.Time
	ch     chan time.Time
	alive  bool
	fired  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.alive && !t.fired
	t.fireAt = t.clock.now.Add(d)
	t.fired = false
	t.alive = true
	return wasActive
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.alive && !t.fired
	t.alive = false
	return wasActive
}

func waitFor(t *testing.T, cond func() bool, max time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", msg)
}

func TestDebouncer_CoalescesBurst(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Unix(0, 0))

	var mu sync.Mutex
	var fired []PendingEvent
	dispatch := func(ev PendingEvent) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
	}

	deb := NewDebouncer(clock, dispatch)
	window := 60 * time.Second
	key := "anatolykoptev/memdb#memdb-go"

	// Three webhooks within 30s — each should reset the timer.
	deb.Submit(key, PendingEvent{Repo: "anatolykoptev/memdb", Service: "memdb-go", CommitSHA: "aaa1111"}, window)
	clock.Advance(10 * time.Second)
	deb.Submit(key, PendingEvent{Repo: "anatolykoptev/memdb", Service: "memdb-go", CommitSHA: "bbb2222"}, window)
	clock.Advance(10 * time.Second)
	deb.Submit(key, PendingEvent{Repo: "anatolykoptev/memdb", Service: "memdb-go", CommitSHA: "ccc3333"}, window)

	// Should still be pending — only 30s elapsed since first event, 0s since last reset.
	if deb.Pending() != 1 {
		t.Fatalf("expected 1 pending entry, got %d", deb.Pending())
	}

	// Advance past the window with no new events.
	clock.Advance(window + time.Second)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(fired) == 1
	}, time.Second, "exactly one dispatch")

	mu.Lock()
	defer mu.Unlock()
	if got := len(fired); got != 1 {
		t.Fatalf("dispatch count = %d, want 1", got)
	}
	if fired[0].CommitSHA != "ccc3333" {
		t.Errorf("dispatched commit = %q, want %q (HEAD at fire time)", fired[0].CommitSHA, "ccc3333")
	}
	if fired[0].HitCount != 3 {
		t.Errorf("HitCount = %d, want 3", fired[0].HitCount)
	}
}

func TestDebouncer_SeparateKeysAreIndependent(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Unix(0, 0))

	var mu sync.Mutex
	var fired []PendingEvent
	dispatch := func(ev PendingEvent) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
	}

	deb := NewDebouncer(clock, dispatch)
	window := 30 * time.Second

	deb.Submit("k1", PendingEvent{Repo: "r1", Service: "s1", CommitSHA: "aaa"}, window)
	deb.Submit("k2", PendingEvent{Repo: "r2", Service: "s2", CommitSHA: "bbb"}, window)

	if deb.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", deb.Pending())
	}

	clock.Advance(window + time.Second)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(fired) == 2
	}, time.Second, "two dispatches")
}

func TestDebouncer_ChangedPathsCapFallback(t *testing.T) {
	t.Parallel()

	t.Run("union under cap carries through", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock(time.Unix(0, 0))
		var mu sync.Mutex
		var fired []PendingEvent
		deb := NewDebouncer(clock, func(ev PendingEvent) { mu.Lock(); fired = append(fired, ev); mu.Unlock() })
		window := 10 * time.Second

		// Two pushes with 5 unique paths each → 10 total, well under 256.
		paths1 := make([]string, 5)
		paths2 := make([]string, 5)
		for i := range 5 {
			paths1[i] = "file-a-" + string(rune('0'+i))
			paths2[i] = "file-b-" + string(rune('0'+i))
		}
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: paths1}, window)
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: paths2}, window)
		clock.Advance(window + time.Second)
		waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(fired) == 1 }, time.Second, "dispatch")

		mu.Lock()
		defer mu.Unlock()
		if fired[0].ChangedPaths == nil {
			t.Error("want non-nil ChangedPaths for union under cap")
		}
		if len(fired[0].ChangedPaths) != 10 {
			t.Errorf("union len = %d, want 10", len(fired[0].ChangedPaths))
		}
	})

	t.Run("single push with 300 paths causes nil", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock(time.Unix(0, 0))
		var mu sync.Mutex
		var fired []PendingEvent
		deb := NewDebouncer(clock, func(ev PendingEvent) { mu.Lock(); fired = append(fired, ev); mu.Unlock() })
		window := 10 * time.Second

		// First push — sets entry with 300 paths.
		big := make([]string, 300)
		for i := range 300 {
			big[i] = "path-" + strings.Repeat("x", 4) + string(rune(i))
		}
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: big}, window)
		// Second push — triggers coalesce; union = 300+ → nil.
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: []string{"extra"}}, window)
		clock.Advance(window + time.Second)
		waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(fired) == 1 }, time.Second, "dispatch")

		mu.Lock()
		defer mu.Unlock()
		if fired[0].ChangedPaths != nil {
			t.Errorf("want nil ChangedPaths when union > cap, got %v (len=%d)", fired[0].ChangedPaths, len(fired[0].ChangedPaths))
		}
	})

	t.Run("three pushes totaling 300 unique paths causes nil", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock(time.Unix(0, 0))
		var mu sync.Mutex
		var fired []PendingEvent
		deb := NewDebouncer(clock, func(ev PendingEvent) { mu.Lock(); fired = append(fired, ev); mu.Unlock() })
		window := 10 * time.Second

		// Push 1: 200 unique paths.
		batch1 := make([]string, 200)
		for i := range 200 {
			batch1[i] = "set1/file-" + string(rune(i))
		}
		// Push 2: 100 additional unique paths (200 + 100 = 300 > 256).
		batch2 := make([]string, 100)
		for i := range 100 {
			batch2[i] = "set2/file-" + string(rune(i))
		}
		// Push 3: 1 more path (union still nil from push 2 coalesce).
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: batch1}, window)
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: batch2}, window)
		deb.Submit("k", PendingEvent{Repo: "r", Service: "s", ChangedPaths: []string{"one-more"}}, window)
		clock.Advance(window + time.Second)
		waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(fired) == 1 }, time.Second, "dispatch")

		mu.Lock()
		defer mu.Unlock()
		if fired[0].ChangedPaths != nil {
			t.Errorf("want nil ChangedPaths for 3-push 300-path burst, got len=%d", len(fired[0].ChangedPaths))
		}
	})
}

func TestDebouncer_ResetExtendsWindow(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Unix(0, 0))

	var mu sync.Mutex
	var fired int
	dispatch := func(PendingEvent) {
		mu.Lock()
		fired++
		mu.Unlock()
	}

	deb := NewDebouncer(clock, dispatch)
	window := 60 * time.Second

	deb.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "a"}, window)
	// 50s pass — almost at the boundary.
	clock.Advance(50 * time.Second)
	// New event resets the timer.
	deb.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "b"}, window)
	// 50s more — original deadline (60s) has passed but reset should hold.
	clock.Advance(50 * time.Second)
	if c := func() int { mu.Lock(); defer mu.Unlock(); return fired }(); c != 0 {
		t.Fatalf("fired prematurely: %d", c)
	}
	// Advance past reset deadline.
	clock.Advance(15 * time.Second)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return fired == 1 }, time.Second, "fire after reset")
}
