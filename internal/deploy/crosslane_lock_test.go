package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLock is a stand-in for the box-wide ci-lock scripts. It records
// acquire/release calls, captures the env each saw (so the test can assert the
// deterministic JOB_KEY matches across the pair), and materialises a real temp
// "slot" dir so the test can assert the slot exists DURING the build and is
// gone AFTER — exercising the REAL acquireCrossLaneLock + defer-release wiring
// in processBuild, not a hand-copied duplicate.
type fakeLock struct {
	mu             sync.Mutex
	acquires       int
	releases       int
	acquireEnv     []string
	releaseEnv     []string
	slotDir        string
	acquireSucceed bool
}

func newFakeLock(t *testing.T, succeed bool) *fakeLock {
	t.Helper()
	dir := t.TempDir()
	slot := filepath.Join(dir, "ci-slot-1")
	return &fakeLock{slotDir: slot, acquireSucceed: succeed}
}

func (f *fakeLock) acquire(_ context.Context, env []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	f.acquireEnv = env
	if !f.acquireSucceed {
		return false, errors.New("ci-lock: timed out after 1s waiting for a free slot")
	}
	if err := os.Mkdir(f.slotDir, 0o755); err != nil && !os.IsExist(err) {
		return false, err
	}
	return true, nil
}

func (f *fakeLock) release(env []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	f.releaseEnv = env
	_ = os.RemoveAll(f.slotDir)
}

func (f *fakeLock) snapshot() (acq, rel int, envAcq, envRel []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquires, f.releases, f.acquireEnv, f.releaseEnv
}

func (f *fakeLock) slotExists() bool {
	_, err := os.Stat(f.slotDir)
	return err == nil
}

// installFakeLock swaps the package-level ci-lock seams to the fake and returns
// a restore func. Also zeros the build delays + stubs buildRunner/upRunner so
// processBuild runs fast and never shells out to real docker (mirrors
// zeroDelays in queue_build_test.go).
func installFakeLock(t *testing.T, fl *fakeLock) func() {
	t.Helper()
	origAcq := ciLockAcquirer
	origRel := ciLockReleaser
	origBuild := buildRunner
	origUp := upRunner
	origHealth := healthWait
	origRetry := upRetryDelay
	origRecovery := portRecoveryWait
	ciLockAcquirer = fl.acquire
	ciLockReleaser = fl.release
	healthWait = 0
	upRetryDelay = 0
	portRecoveryWait = 0
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	return func() {
		ciLockAcquirer = origAcq
		ciLockReleaser = origRel
		buildRunner = origBuild
		upRunner = origUp
		healthWait = origHealth
		upRetryDelay = origRetry
		portRecoveryWait = origRecovery
	}
}

func heavyReq() BuildRequest {
	return BuildRequest{
		Repo:      "owner/rust-svc",
		CommitSHA: "deadbeefcafe",
		Config: RepoConfig{
			ComposePath: "/tmp",
			Services:    []string{"svc"},
			Heavy:       true,
		},
	}
}

func lightReq() BuildRequest {
	r := heavyReq()
	r.Config.Heavy = false
	return r
}

// envRunID extracts the GITHUB_RUN_ID value from the lock env slice so the
// test can assert acquire and release computed the SAME JOB_KEY.
func envRunID(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "GITHUB_RUN_ID=") {
			return strings.TrimPrefix(kv, "GITHUB_RUN_ID=")
		}
	}
	return ""
}

// TestCrossLaneLock_HeavyAcquireThenRelease asserts the real processBuild
// wiring acquires the box-wide lock BEFORE the build runs (slot exists while
// buildRunner is inside) and releases it AFTER (slot gone, release called
// exactly once), with acquire/release sharing the same JOB_KEY.
func TestCrossLaneLock_HeavyAcquireThenRelease(t *testing.T) {
	fl := newFakeLock(t, true)
	restore := installFakeLock(t, fl)
	defer restore()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var sawSlotDuringBuild bool
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		sawSlotDuringBuild = fl.slotExists()
		entered <- struct{}{}
		<-release
		return []byte("ok"), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	done := make(chan struct{}, 1)
	go func() { q.processBuild(ctx, heavyReq(), true); done <- struct{}{} }()

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("timeout waiting for build to enter buildRunner")
	}

	if !sawSlotDuringBuild {
		t.Fatal("lock slot did NOT exist during heavy build — acquire did not run before the build")
	}
	if acq, _, _, _ := fl.snapshot(); acq != 1 {
		t.Fatalf("expected 1 acquire before build, got %d", acq)
	}

	close(release)
	<-done

	if fl.slotExists() {
		t.Fatal("lock slot still exists after build — release did not run")
	}
	acq, rel, envAcq, envRel := fl.snapshot()
	if acq != 1 || rel != 1 {
		t.Fatalf("expected acquire=1 release=1, got acquire=%d release=%d", acq, rel)
	}
	if envRunID(envAcq) == "" || envRunID(envAcq) != envRunID(envRel) {
		t.Fatalf("acquire/release JOB_KEY mismatch: acquire=%q release=%q",
			envRunID(envAcq), envRunID(envRel))
	}
}

// TestCrossLaneLock_ReleasedOnBuildError is the highest-value test: a heavy
// build that ERRORS must still release the lock (defer fires on the error
// return path of processBuild, not just the success path).
func TestCrossLaneLock_ReleasedOnBuildError(t *testing.T) {
	fl := newFakeLock(t, true)
	restore := installFakeLock(t, fl)
	defer restore()

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("build failed"), errors.New("exit status 1")
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	q.processBuild(ctx, heavyReq(), true)

	acq, rel, _, _ := fl.snapshot()
	if acq != 1 {
		t.Fatalf("expected 1 acquire, got %d", acq)
	}
	if rel != 1 {
		t.Fatalf("lock NOT released after build error (defer path broken): releases=%d", rel)
	}
	if fl.slotExists() {
		t.Fatal("lock slot still exists after errored build — release did not remove it")
	}
}

// TestCrossLaneLock_ReleasedOnPanic asserts the defer-release survives a panic
// in the build path (deferred funcs run during panic unwinding). The panic is
// recovered in the test goroutine so it does not crash the process.
func TestCrossLaneLock_ReleasedOnPanic(t *testing.T) {
	fl := newFakeLock(t, true)
	restore := installFakeLock(t, fl)
	defer restore()

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		panic("simulated cargo build panic")
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	var panicked any
	func() {
		defer func() { panicked = recover() }()
		q.processBuild(ctx, heavyReq(), true)
	}()

	if panicked == nil {
		t.Fatal("expected buildRunner panic to propagate (test harness must see it)")
	}
	acq, rel, _, _ := fl.snapshot()
	if acq != 1 {
		t.Fatalf("expected 1 acquire, got %d", acq)
	}
	if rel != 1 {
		t.Fatalf("lock NOT released on panic (defer did not fire during unwind): releases=%d", rel)
	}
	if fl.slotExists() {
		t.Fatal("lock slot still exists after panicked build — release did not remove it")
	}
}

// TestCrossLaneLock_NonHeavySkipsLock asserts a non-heavy build never touches
// the cross-lane lock — cheap go/static deploys stay unblocked.
func TestCrossLaneLock_NonHeavySkipsLock(t *testing.T) {
	fl := newFakeLock(t, true)
	restore := installFakeLock(t, fl)
	defer restore()

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("ok"), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	q.processBuild(ctx, lightReq(), false)

	acq, rel, _, _ := fl.snapshot()
	if acq != 0 || rel != 0 {
		t.Fatalf("non-heavy build touched the cross-lane lock: acquire=%d release=%d", acq, rel)
	}
}

// TestCrossLaneLock_AcquireTimeoutProceedsUnlocked asserts the fail-safe: when
// the acquire returns non-zero (saturated lock / timeout) the build PROCEEDS
// and a release is still attempted (no-op on missing state). Uses a fake that
// fails acquire immediately so the test does not wait the real 45m.
func TestCrossLaneLock_AcquireTimeoutProceedsUnlocked(t *testing.T) {
	fl := newFakeLock(t, false) // acquire always fails
	restore := installFakeLock(t, fl)
	defer restore()

	buildRan := false
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		buildRan = true
		return []byte("ok"), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	q.processBuild(ctx, heavyReq(), true)

	if !buildRan {
		t.Fatal("build did NOT proceed after cross-lane lock acquire timeout — deploy would stall")
	}
	acq, rel, _, _ := fl.snapshot()
	if acq != 1 {
		t.Fatalf("expected 1 acquire attempt, got %d", acq)
	}
	if rel != 1 {
		t.Fatalf("expected fail-safe release attempt after timeout, got releases=%d", rel)
	}
}
