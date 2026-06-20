package deploy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newPersistingStoppedQueue builds a Queue whose worker has already exited
// (so recovered/submitted entries land in q.pending without docker draining
// them) wired to a persist store at a temp path. Mirrors newPersistingDebouncer.
func newPersistingStoppedQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	q := newStoppedQueue(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-queue.json")
	q.WithPersistence(path)
	return q, path
}

func queueReq(repo, svc, sha string) BuildRequest {
	return BuildRequest{
		Repo:      repo,
		CommitSHA: sha,
		Config:    RepoConfig{Services: []string{svc}, SourcePath: "/tmp/nonexistent-src"},
	}
}

// TestQueuePersist_SubmitWritesStateFile verifies that Submit persists the
// queued entry to disk so a restart can recover it.
func TestQueuePersist_SubmitWritesStateFile(t *testing.T) {
	q, path := newPersistingStoppedQueue(t)

	if !q.Submit(queueReq("anatolykoptev/memdb", "memdb-go", "aaa1111deadbeef")) {
		t.Fatal("expected Submit to enqueue (return true)")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected state file written by Submit, got err: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("state file is empty after Submit")
	}
}

// TestQueuePersist_RecoverRequeuesQueuedBuild is the core regression test for
// the VOLATILE-PENDING-STATE class at the queue layer: a QUEUED build (in
// q.pending, never picked) survives a process restart. We Submit (which
// persists), throw away the Queue, then RecoverQueue into a fresh one and
// confirm the build is re-enqueued as pending.
func TestQueuePersist_RecoverRequeuesQueuedBuild(t *testing.T) {
	q1, path := newPersistingStoppedQueue(t)
	if !q1.Submit(queueReq("r", "svc", "queuedSHA1234567")) {
		t.Fatal("submit must enqueue")
	}

	// Fresh queue (simulating a restarted dozor) reloads the persisted entry.
	q2 := newStoppedQueue(t)
	q2.WithPersistence(path)
	// SHA resolver returns a DIFFERENT sha → not stale → must re-enqueue.
	q2.shaResolver = func(_ context.Context, _ string) string { return "oldDEPLOYED" }

	if err := q2.RecoverQueue(context.Background()); err != nil {
		t.Fatalf("RecoverQueue failed: %v", err)
	}

	q2.mu.Lock()
	got, has := q2.pending["svc"]
	q2.mu.Unlock()
	if !has {
		t.Fatal("recovered queued build must be re-enqueued as pending, got none")
	}
	if got.CommitSHA != "queuedSHA1234567" {
		t.Fatalf("recovered pending SHA = %q, want persisted SHA", got.CommitSHA)
	}
}

// TestQueuePersist_RecoverRequeuesInFlightBuild verifies the in-flight
// semantics: a build that was BUSY (a docker build running) at restart was
// INTERRUPTED, and must be re-enqueued so the half-done deploy completes.
func TestQueuePersist_RecoverRequeuesInFlightBuild(t *testing.T) {
	q1, path := newPersistingStoppedQueue(t)
	// Simulate a build that reached in-flight state when dozor was killed.
	req := queueReq("r", "svc", "inflightSHA98765")
	q1.mu.Lock()
	q1.busySHA["svc"] = req.CommitSHA
	q1.busyReq["svc"] = req
	q1.building["svc"] = true
	q1.persistLocked()
	q1.mu.Unlock()

	q2 := newStoppedQueue(t)
	q2.WithPersistence(path)
	q2.shaResolver = func(_ context.Context, _ string) string { return "deployed-old" }

	if err := q2.RecoverQueue(context.Background()); err != nil {
		t.Fatalf("RecoverQueue failed: %v", err)
	}

	q2.mu.Lock()
	got, has := q2.pending["svc"]
	q2.mu.Unlock()
	if !has {
		t.Fatal("interrupted in-flight build must be re-enqueued as pending, got none")
	}
	if got.CommitSHA != "inflightSHA98765" {
		t.Fatalf("recovered in-flight SHA = %q, want persisted SHA", got.CommitSHA)
	}
}

// TestQueuePersist_RecoverStaleSkip verifies the no-stale-rebuild guard: a
// recovered entry whose commit is already the deployed HEAD is dropped, not
// rebuilt.
func TestQueuePersist_RecoverStaleSkip(t *testing.T) {
	q1, path := newPersistingStoppedQueue(t)
	if !q1.Submit(queueReq("r", "svc", "abcdef1234567890")) {
		t.Fatal("submit must enqueue")
	}

	q2 := newStoppedQueue(t)
	q2.WithPersistence(path)
	// Deployed HEAD == persisted SHA (short form) → stale, must skip.
	q2.shaResolver = func(_ context.Context, _ string) string { return ShortSHA("abcdef1234567890") }

	before := testutil.ToFloat64(QueuePersistTotal.WithLabelValues("r", "svc", "stale_skip"))
	if err := q2.RecoverQueue(context.Background()); err != nil {
		t.Fatalf("RecoverQueue failed: %v", err)
	}

	q2.mu.Lock()
	_, has := q2.pending["svc"]
	q2.mu.Unlock()
	if has {
		t.Fatal("stale build (already deployed) must NOT be re-enqueued")
	}
	if delta := testutil.ToFloat64(QueuePersistTotal.WithLabelValues("r", "svc", "stale_skip")) - before; delta != 1 {
		t.Fatalf("stale_skip counter delta = %v, want 1", delta)
	}
}

// TestQueuePersist_MissingFileTolerated verifies a clean boot with no state
// file does not error and does not crash.
func TestQueuePersist_MissingFileTolerated(t *testing.T) {
	q := newStoppedQueue(t)
	q.WithPersistence(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err := q.RecoverQueue(context.Background()); err != nil {
		t.Fatalf("missing file must be tolerated, got: %v", err)
	}
}

// TestQueuePersist_CorruptFileTolerated verifies a corrupt state file is
// logged + counted (reload_error) + dropped, never crashing the orchestrator.
func TestQueuePersist_CorruptFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-queue.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	q := newStoppedQueue(t)
	q.WithPersistence(path)
	q.shaResolver = func(_ context.Context, _ string) string { return "x" }

	before := testutil.ToFloat64(QueuePersistTotal.WithLabelValues("", "", "reload_error"))
	if err := q.RecoverQueue(context.Background()); err != nil {
		t.Fatalf("corrupt file must be tolerated (log + continue), got: %v", err)
	}
	if delta := testutil.ToFloat64(QueuePersistTotal.WithLabelValues("", "", "reload_error")) - before; delta != 1 {
		t.Fatalf("reload_error counter delta = %v, want 1", delta)
	}
}

// TestQueuePersist_WorkersGatedUntilRecovery closes the HIGH-1 race the
// code-quality review flagged: in production the worker goroutines are LIVE
// from NewQueueN, so without a gate a worker could drain a survivor before
// RecoverQueue finished re-enqueuing the full set (and before the debounce
// reload). This test uses a REAL worker (not newStoppedQueue): with
// persistence enabled the worker must NOT drain a pending entry until
// RecoverQueue opens the drain gate.
func TestQueuePersist_WorkersGatedUntilRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	var drained bool
	notify := func(string) { // notify fires from processBuild → proves a drain happened
		mu.Lock()
		drained = true
		mu.Unlock()
	}

	q := NewQueueN(ctx, notify, 1)
	defer q.Close()
	// Enable persistence → re-arms the drain gate (workers now wait for RecoverQueue).
	path := filepath.Join(t.TempDir(), "deploy-queue.json")
	q.WithPersistence(path)

	// Inject a pending build directly and wake the worker. With the gate armed
	// the worker is blocked BEFORE its drain loop, so it must not pick this up.
	req := queueReq("r", "svc", "gatedSHA1234567")
	q.mu.Lock()
	q.pending["svc"] = req
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}

	// Give a real worker time to (wrongly) drain if the gate were not honoured.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	drainedEarly := drained
	mu.Unlock()
	if drainedEarly {
		t.Fatal("worker drained a build BEFORE RecoverQueue opened the gate (HIGH-1 race)")
	}
	q.mu.Lock()
	_, stillPending := q.pending["svc"]
	q.mu.Unlock()
	if !stillPending {
		t.Fatal("pending entry vanished before recovery — worker bypassed the gate")
	}

	// Open the gate. There is no state file (nothing persisted on this fresh
	// queue), so RecoverQueue just opens the gate; the worker then drains.
	if err := q.RecoverQueue(ctx); err != nil {
		t.Fatalf("RecoverQueue failed: %v", err)
	}
	// Re-signal in case the earlier signal was consumed while gated.
	select {
	case q.signal <- struct{}{}:
	default:
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := drained
		mu.Unlock()
		if ok {
			return // worker drained after the gate opened — correct
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not drain after RecoverQueue opened the gate")
}

// TestQueuePersist_NoDoubleRecoveryWithDebounce verifies the interaction with
// the debounce-layer recovery (#110): a build that was recovered into the queue
// AND then "fired" by a debounce reload for the SAME (service, SHA) produces
// exactly ONE pending build, because both arrive via Submit which dedups.
// RecoverQueue runs first (as at boot), then a second Submit (the debounce
// fire) for the identical commit must be deduplicated.
func TestQueuePersist_NoDoubleRecoveryWithDebounce(t *testing.T) {
	q1, path := newPersistingStoppedQueue(t)
	if !q1.Submit(queueReq("r", "svc", "sharedSHA7654321")) {
		t.Fatal("submit must enqueue")
	}

	q2 := newStoppedQueue(t)
	q2.WithPersistence(path)
	q2.shaResolver = func(_ context.Context, _ string) string { return "different" }

	if err := q2.RecoverQueue(context.Background()); err != nil {
		t.Fatalf("RecoverQueue failed: %v", err)
	}
	// The debounce reload now "fires" the same commit for the same service.
	// Submit must dedup it against the already-recovered pending entry.
	if q2.Submit(queueReq("r", "svc", "sharedSHA7654321")) {
		t.Fatal("second Submit of identical (service, SHA) must dedup, not enqueue twice")
	}

	q2.mu.Lock()
	got, has := q2.pending["svc"]
	q2.mu.Unlock()
	if !has || got.CommitSHA != "sharedSHA7654321" {
		t.Fatalf("expected exactly one pending entry for the shared SHA, has=%v sha=%q", has, got.CommitSHA)
	}
}
