package deploy

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Clock abstracts time.Now / time.NewTimer so tests can advance virtual time.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is the subset of time.Timer the debouncer relies on.
type Timer interface {
	C() <-chan time.Time
	Reset(d time.Duration) bool
	Stop() bool
}

// realClock is the default production Clock.
type realClock struct{}

func (realClock) Now() time.Time                     { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer     { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time     { return r.t.C }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
func (r *realTimer) Stop() bool               { return r.t.Stop() }

// PendingEvent is the most recent webhook payload for a given debounce key.
// On timer fire, the debouncer hands this back to the dispatcher so the build
// uses HEAD at fire time, not the first webhook's commit.
type PendingEvent struct {
	Repo      string
	Service   string
	CommitSHA string
	Config    RepoConfig
	HitCount  int       // number of webhooks coalesced into this pending build
	FirstSeen time.Time // when the first webhook of this batch arrived
	LastSeen  time.Time // when the most recent webhook arrived
}

// DispatchFunc is invoked once per debounced batch, after the quiet window elapses.
type DispatchFunc func(PendingEvent)

// Debouncer coalesces a burst of webhook events per (repo+service) key into a
// single build dispatched after `window` of silence from the last event.
//
// Concurrency model: callers invoke Submit from any goroutine. Each key has at
// most one active timer goroutine. Timers are restarted on every Submit; on
// fire, the dispatcher is called synchronously from the timer goroutine.
type Debouncer struct {
	clock    Clock
	dispatch DispatchFunc

	mu      sync.Mutex
	pending map[string]*pendingEntry
}

type pendingEntry struct {
	event  PendingEvent
	timer  Timer
	cancel chan struct{} // closed to abort an in-flight wait (test/shutdown)
}

// NewDebouncer creates a Debouncer that calls `dispatch` after each quiet window.
// Pass nil for clock to use the real wall clock.
func NewDebouncer(clock Clock, dispatch DispatchFunc) *Debouncer {
	if clock == nil {
		clock = realClock{}
	}
	return &Debouncer{
		clock:    clock,
		dispatch: dispatch,
		pending:  make(map[string]*pendingEntry),
	}
}

// Submit records a webhook event for the given key. If a build is already
// pending for that key, the existing timer is reset and the latest commit
// metadata replaces the previous (so the eventual build uses HEAD at fire
// time, not the first webhook).
func (d *Debouncer) Submit(key string, ev PendingEvent, window time.Duration) {
	d.mu.Lock()
	now := d.clock.Now()

	if entry, ok := d.pending[key]; ok {
		// Reset existing timer with the freshest commit metadata.
		entry.event.CommitSHA = ev.CommitSHA
		entry.event.Config = ev.Config
		entry.event.HitCount++
		entry.event.LastSeen = now
		entry.timer.Reset(window)
		hits := entry.event.HitCount
		d.mu.Unlock()
		DebouncedTotal.WithLabelValues(ev.Repo, ev.Service).Inc()
		slog.Info("deploy debounced: webhook received, resetting timer",
			"repo", ev.Repo,
			"service", ev.Service,
			"hit", hits,
			"window", window.String(),
			"commit", short(ev.CommitSHA),
		)
		return
	}

	ev.HitCount = 1
	ev.FirstSeen = now
	ev.LastSeen = now
	cancel := make(chan struct{})
	entry := &pendingEntry{
		event:  ev,
		timer:  d.clock.NewTimer(window),
		cancel: cancel,
	}
	d.pending[key] = entry
	d.mu.Unlock()

	DebouncedTotal.WithLabelValues(ev.Repo, ev.Service).Inc()
	slog.Info("deploy debounced: first webhook in burst, starting timer",
		"repo", ev.Repo,
		"service", ev.Service,
		"window", window.String(),
		"commit", short(ev.CommitSHA),
	)

	go d.waitAndFire(key, entry)
}

// waitAndFire blocks on the entry's timer (and cancellation) and then fires.
func (d *Debouncer) waitAndFire(key string, entry *pendingEntry) {
	select {
	case <-entry.timer.C():
	case <-entry.cancel:
		entry.timer.Stop()
		return
	}

	d.mu.Lock()
	cur, ok := d.pending[key]
	if !ok || cur != entry {
		d.mu.Unlock()
		return
	}
	delete(d.pending, key)
	ev := entry.event
	d.mu.Unlock()

	slog.Info("deploy fired after debounce",
		"repo", ev.Repo,
		"service", ev.Service,
		"hits", ev.HitCount,
		"commit", short(ev.CommitSHA),
		"first_seen", ev.FirstSeen.Format(time.RFC3339),
		"last_seen", ev.LastSeen.Format(time.RFC3339),
	)
	d.dispatch(ev)
}

// Stop cancels every pending timer without firing. Used at shutdown so we do
// not block on goroutines waiting for wall-clock timers.
func (d *Debouncer) Stop(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, entry := range d.pending {
		close(entry.cancel)
		delete(d.pending, key)
	}
	_ = ctx
}

// Pending returns the number of in-flight debounce keys (test helper).
func (d *Debouncer) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}
