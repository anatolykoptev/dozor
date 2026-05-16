package deploy

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestQueueN_ConcurrentBuilds verifies that NewQueueN(ctx, notify, 2) allows
// two builds to run simultaneously, while NewQueueN(ctx, notify, 1) serializes
// them. Uses channel-based synchronization — no time.Sleep timing assertions.
func TestQueueN_ConcurrentBuilds(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		wantOverlap bool
	}{
		{"serial_n1", 1, false},
		{"concurrent_n2", 2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// entered: one token per build that has started running
			entered := make(chan struct{}, 10)
			// release: closed to unblock all running builds
			release := make(chan struct{})

			origBuild := buildRunner
			origUp := upRunner
			origHealth := healthWait
			origRetry := upRetryDelay
			origRecovery := portRecoveryWait
			t.Cleanup(func() {
				buildRunner = origBuild
				upRunner = origUp
				healthWait = origHealth
				upRetryDelay = origRetry
				portRecoveryWait = origRecovery
			})
			// Zero delays so tests complete quickly.
			healthWait = 0
			upRetryDelay = 0
			portRecoveryWait = 0

			var inFlight int64
			var peak int64
			// Block in the build step so we can observe concurrency.
			buildRunner = func(ctx context.Context, dir string, args []string) ([]byte, error) {
				cur := atomic.AddInt64(&inFlight, 1)
				for {
					old := atomic.LoadInt64(&peak)
					if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
						break
					}
				}
				entered <- struct{}{}
				<-release
				atomic.AddInt64(&inFlight, -1)
				return []byte("mock"), nil
			}
			upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
				return nil, nil
			}

			q := NewQueueN(ctx, func(string) {}, tt.concurrency)
			defer q.Close()

			// Submit two builds for different service groups (different keys).
			// SourcePath="" skips git steps (see gitPrepare: early return on empty path).
			for _, svc := range []string{"svc-a", "svc-b"} {
				q.Submit(BuildRequest{
					Repo:      "test/repo",
					CommitSHA: "abc1234567",
					Config: RepoConfig{
						ComposePath: "/tmp",
						Services:    []string{svc},
					},
				})
			}

			// Wait for first build to enter buildRunner.
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("timeout waiting for first build to start")
			}

			if tt.wantOverlap {
				// concurrency=2: second build should also start before we release.
				select {
				case <-entered:
					// both running concurrently — correct
				case <-time.After(3 * time.Second):
					t.Fatal("concurrency=2: second build did not start before first completed")
				}
			} else {
				// concurrency=1: second build must NOT start before first completes.
				select {
				case <-entered:
					t.Fatal("concurrency=1: second build started before first completed (not serialized)")
				case <-time.After(200 * time.Millisecond):
					// Good: serial as expected.
				}
			}

			// Unblock all builds.
			close(release)

			if tt.wantOverlap {
				if p := atomic.LoadInt64(&peak); p < 2 {
					t.Errorf("concurrency=2: peak concurrent builds = %d, want >= 2", p)
				}
			}
		})
	}
}

// TestQueueN_HeavySemaphore verifies that two heavy builds are serialized even
// when concurrency=2, preventing OOM on the ARM host.
func TestQueueN_HeavySemaphore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	release := make(chan struct{})
	entered := make(chan struct{}, 10)

	origBuild := buildRunner
	origUp := upRunner
	origHealth := healthWait
	origRetry := upRetryDelay
	origRecovery := portRecoveryWait
	t.Cleanup(func() {
		buildRunner = origBuild
		upRunner = origUp
		healthWait = origHealth
		upRetryDelay = origRetry
		portRecoveryWait = origRecovery
	})
	healthWait = 0
	upRetryDelay = 0
	portRecoveryWait = 0

	buildRunner = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		entered <- struct{}{}
		<-release
		return []byte("ok"), nil
	}
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}

	q := NewQueueN(ctx, func(string) {}, 2)
	defer q.Close()

	// Submit two heavy builds for different service groups.
	for _, svc := range []string{"heavy-a", "heavy-b"} {
		q.Submit(BuildRequest{
			Repo:      "test/repo",
			CommitSHA: "abc1234567",
			Config: RepoConfig{
				ComposePath: "/tmp",
				Services:    []string{svc},
				Heavy:       true,
			},
		})
	}

	// First heavy build enters.
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("timeout waiting for first heavy build")
	}

	// Second heavy build must NOT start while first is running (heavySem=1).
	select {
	case <-entered:
		t.Fatal("two heavy builds ran concurrently — heavySem not enforced")
	case <-time.After(200 * time.Millisecond):
		// Correct: serialized.
	}

	close(release)
}

// TestDispatch_SkipDebounceWhenBuildActive verifies that a second webhook for
// the same service group bypasses the debouncer when a build is active, then
// correctly routes through the debouncer when the queue is idle.
func TestDispatch_SkipDebounceWhenBuildActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := NewQueueN(ctx, func(string) {}, 1)
	defer q.Close()

	// Simulate an active build for "svc-x".
	q.mu.Lock()
	q.busySHA["svc-x"] = "active-sha"
	q.building["svc-x"] = true
	q.mu.Unlock()

	debouncer := NewDebouncer(nil, func(PendingEvent) {})
	t.Cleanup(func() { debouncer.Stop(ctx) })

	h := &Handler{
		config:    &Config{},
		queue:     q,
		notify:    func(string) {},
		debouncer: debouncer,
	}

	push := pushEvent{
		Repository: struct {
			FullName string `json:"full_name"`
		}{FullName: "owner/repo"},
		HeadCommit: struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}{ID: "new-sha"},
	}
	rc := &RepoConfig{
		Services:        []string{"svc-x"},
		DebounceSeconds: 30,
		ComposePath:     "/tmp",
	}

	// When build is active, dispatchPush should bypass debouncer.
	status := h.dispatchPush(push, rc)
	if status == "debounced" {
		t.Errorf("dispatchPush returned %q when build is active; want bypass (queued/deduplicated)", status)
	}
	if n := debouncer.Pending(); n != 0 {
		t.Errorf("debouncer.Pending() = %d, want 0 (debouncer should be bypassed when build active)", n)
	}

	// Clear active build AND drain the pending entry that Submit added above.
	q.mu.Lock()
	delete(q.busySHA, "svc-x")
	delete(q.building, "svc-x")
	delete(q.pending, "svc-x") // drain pending from the first Submit call
	q.mu.Unlock()

	// With queue idle, next push should go through debouncer.
	push.HeadCommit.ID = "cold-sha"
	status2 := h.dispatchPush(push, rc)
	if status2 != "debounced" {
		t.Errorf("dispatchPush returned %q when queue is idle; want \"debounced\"", status2)
	}
	if n := debouncer.Pending(); n != 1 {
		t.Errorf("debouncer.Pending() = %d, want 1 after idle dispatch", n)
	}
}
