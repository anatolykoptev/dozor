package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestSession creates a minimal Session suitable for manager tests.
// It initialises only the fields that Manager.Get / Close actually touch:
//   - closed    (atomic.Bool)
//   - idleTimer (must be non-nil; Close calls idleTimer.Stop)
//   - cancel    (must be non-nil; Close calls it)
//   - stdinMu + stdin (must be non-nil; Close calls stdin.Close)
func newTestSession() *Session {
	return &Session{
		cancel:      func() {},
		stdin:       nopWriteCloser{},
		idleTimer:   time.NewTimer(time.Hour), // won't fire during tests
		idleTimeout: time.Hour,
	}
}

// nopWriteCloser satisfies io.WriteCloser with no-ops.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// TestManager_SetGet verifies that Set stores a session and Get retrieves it.
func TestManager_SetGet(t *testing.T) {
	m := NewManager()
	s := newTestSession()

	m.Set("chat1", s)

	got := m.Get("chat1")
	if got != s {
		t.Fatalf("Get returned %v, want %v", got, s)
	}
}

// TestManager_GetNil verifies that Get on an unknown chat ID returns nil.
func TestManager_GetNil(t *testing.T) {
	m := NewManager()

	got := m.Get("nonexistent")
	if got != nil {
		t.Fatalf("Get returned non-nil for nonexistent chat ID: %v", got)
	}
}

// TestManager_GetClosedReturnsNil verifies that Get auto-removes and returns nil
// for a session whose closed flag is already set.
func TestManager_GetClosedReturnsNil(t *testing.T) {
	m := NewManager()
	s := newTestSession()
	s.closed.Store(true) // mark as closed before registering

	// Bypass Close so we can set the flag manually without needing a live timer.
	m.mu.Lock()
	m.sessions["chat1"] = s
	m.mu.Unlock()

	got := m.Get("chat1")
	if got != nil {
		t.Fatalf("Get returned non-nil for closed session: %v", got)
	}

	// Entry should have been pruned from the map.
	m.mu.Lock()
	_, exists := m.sessions["chat1"]
	m.mu.Unlock()
	if exists {
		t.Fatal("closed session was not removed from the map by Get")
	}
}

// TestManager_Delete verifies that Delete closes the session and Get returns nil afterward.
func TestManager_Delete(t *testing.T) {
	m := NewManager()
	s := newTestSession()

	m.Set("chat1", s)
	m.Delete("chat1")

	if !s.closed.Load() {
		t.Fatal("session.closed should be true after Delete")
	}

	got := m.Get("chat1")
	if got != nil {
		t.Fatalf("Get after Delete returned non-nil: %v", got)
	}
}

// TestManager_DeleteNonexistent verifies that Delete on an unknown ID is a no-op.
func TestManager_DeleteNonexistent(t *testing.T) {
	m := NewManager()
	// Should not panic.
	m.Delete("ghost")
}

// TestManager_SetReplacesExisting verifies that Set on an already-registered chat ID
// closes the old session and stores the new one.
func TestManager_SetReplacesExisting(t *testing.T) {
	m := NewManager()
	old := newTestSession()
	newer := newTestSession()

	m.Set("chat1", old)
	m.Set("chat1", newer) // should close old

	if !old.closed.Load() {
		t.Fatal("old session should have been closed when replaced")
	}

	got := m.Get("chat1")
	if got != newer {
		t.Fatalf("Get returned %v, want newer session %v", got, newer)
	}
}

// TestManager_CloseAll verifies that CloseAll closes every registered session and
// empties the manager.
func TestManager_CloseAll(t *testing.T) {
	const count = 5

	m := NewManager()
	sessions := make([]*Session, count)
	for i := range sessions {
		sessions[i] = newTestSession()
		m.Set(string(rune('A'+i)), sessions[i])
	}

	m.CloseAll()

	for i, s := range sessions {
		if !s.closed.Load() {
			t.Errorf("session[%d] not closed after CloseAll", i)
		}
	}

	// Map must be empty after CloseAll.
	m.mu.Lock()
	remaining := len(m.sessions)
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("manager still has %d session(s) after CloseAll", remaining)
	}
}

// TestManager_CloseAllEmpty verifies that CloseAll on an empty manager is a no-op.
func TestManager_CloseAllEmpty(t *testing.T) {
	m := NewManager()
	// Should not panic.
	m.CloseAll()
}

// TestManager_ConcurrentSetGetDelete exercises the manager under parallel access
// to catch data races (run with -race).
func TestManager_ConcurrentSetGetDelete(t *testing.T) {
	const goroutines = 20
	const iterations = 100

	m := NewManager()
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			chatID := string(rune('A' + id%26))
			for range iterations {
				s := newTestSession()
				m.Set(chatID, s)
				_ = m.Get(chatID)
				m.Delete(chatID)
			}
		}(i)
	}

	wg.Wait()
}

// TestManager_ConcurrentCloseAll races CloseAll against ongoing Set/Get operations.
func TestManager_ConcurrentCloseAll(t *testing.T) {
	m := NewManager()

	var wg sync.WaitGroup
	var closed atomic.Bool

	// Writer goroutine continuously sets sessions until CloseAll fires.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !closed.Load() {
			m.Set("writer", newTestSession())
			_ = m.Get("writer")
		}
	}()

	// Give the writer a moment to start, then close all.
	time.Sleep(5 * time.Millisecond)
	m.CloseAll()
	closed.Store(true)

	wg.Wait()
}
