package deploy

import (
	"context"
	"testing"
)

// newStoppedQueue creates a Queue whose worker exits before draining any
// pending submits. Tests can then inspect Snapshot() without races against
// processBuild (which would try to invoke docker compose on /tmp).
func newStoppedQueue(t *testing.T) *Queue {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(ctx, func(string) {})
	cancel()
	<-q.done // worker fully exited before any Submit calls
	t.Cleanup(func() {
		// Close handles activeQueue clearing.
		// q.Close on an already-cancelled queue is idempotent.
	})
	return q
}

func TestQueue_Snapshot_Empty(t *testing.T) {
	q := newStoppedQueue(t)

	got := q.Snapshot()
	if got != nil {
		t.Errorf("expected nil snapshot on empty queue, got %d entries", len(got))
	}
}

func TestQueue_Snapshot_Pending(t *testing.T) {
	q := newStoppedQueue(t)

	req := BuildRequest{
		Repo:      "anatolykoptev/oxpulse-chat",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			Services:    []string{"oxpulse-chat"},
			ComposePath: "/tmp/nx",
			SourcePath:  "/tmp/nx",
		},
	}
	if !q.Submit(req) {
		t.Fatal("Submit expected to return true on empty queue")
	}

	snap := q.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 snapshot entry, got %d", len(snap))
	}
	s := snap[0]
	if len(s.Services) != 1 || s.Services[0] != "oxpulse-chat" {
		t.Errorf("services: got %v, want [oxpulse-chat]", s.Services)
	}
	if s.PendingSHA != "abc1234567890" {
		t.Errorf("pending SHA: got %q", s.PendingSHA)
	}
	if s.BuildingSHA != "" {
		t.Errorf("building SHA should be empty (worker stopped), got %q", s.BuildingSHA)
	}
}

func TestQueue_Snapshot_MultiServiceKey(t *testing.T) {
	// serviceKey joins with '+'; verify the snapshot splits it back correctly.
	q := newStoppedQueue(t)

	req := BuildRequest{
		Repo:      "anatolykoptev/vaelor",
		CommitSHA: "def987654321",
		Config: RepoConfig{
			Services:    []string{"vaelor-orchestrator", "vaelor-content"},
			ComposePath: "/tmp/nx",
			SourcePath:  "/tmp/nx",
		},
	}
	q.Submit(req)

	snap := q.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(snap))
	}
	want := []string{"vaelor-orchestrator", "vaelor-content"}
	if len(snap[0].Services) != 2 {
		t.Fatalf("expected 2 services, got %v", snap[0].Services)
	}
	for i, s := range want {
		if snap[0].Services[i] != s {
			t.Errorf("service[%d]: got %q, want %q", i, snap[0].Services[i], s)
		}
	}
}

func TestActiveQueue_PointerLifecycle(t *testing.T) {
	// Confirm the singleton tracks the most-recent NewQueue and Close clears
	// only its own pointer (not someone else's).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q1 := NewQueue(ctx, func(string) {})
	if ActiveQueue() != q1 {
		t.Fatal("ActiveQueue should be q1 after construction")
	}

	q2 := NewQueue(ctx, func(string) {})
	if ActiveQueue() != q2 {
		t.Fatal("ActiveQueue should switch to q2 (last-writer-wins)")
	}

	// Closing q1 should NOT touch the pointer (q2 owns it now).
	q1.Close()
	if ActiveQueue() != q2 {
		t.Errorf("q1.Close cleared pointer that belonged to q2")
	}

	// Closing q2 clears the pointer.
	q2.Close()
	if ActiveQueue() != nil {
		t.Errorf("q2.Close should have cleared the pointer, got %v", ActiveQueue())
	}
}
