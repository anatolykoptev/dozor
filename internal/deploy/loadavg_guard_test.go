package deploy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// installFakeLoadGuard swaps the loadavg-reader seam + poll interval + wait cap
// to test-controlled values and returns a restore func. Tests MUST override the
// poll interval to something tiny — they MUST NOT actually sleep for real
// seconds. Mirrors installFakeLock's save/restore pattern.
func installFakeLoadGuard(t *testing.T, reader func() (float64, error), poll time.Duration, waitSecs int) func() {
	t.Helper()
	origReader := loadavgReader
	origPoll := loadGuardPollInterval
	origWait := loadGuardWaitSecs
	origMax := loadGuardMaxLoad
	loadavgReader = reader
	loadGuardPollInterval = poll
	loadGuardWaitSecs = waitSecs
	loadGuardMaxLoad = 8.0 // fixed threshold so tests don't depend on runtime.NumCPU
	return func() {
		loadavgReader = origReader
		loadGuardPollInterval = origPoll
		loadGuardWaitSecs = origWait
		loadGuardMaxLoad = origMax
	}
}

// TestLoadGuard_ProceedsImmediately: fake reader below threshold → proceeds
// immediately, {outcome="proceeded_immediately"} +1.
func TestLoadGuard_ProceedsImmediately(t *testing.T) {
	restore := installFakeLoadGuard(t,
		func() (float64, error) { return 0.5, nil }, // below 8.0
		time.Millisecond, 10)
	defer restore()

	before := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_immediately"))
	outcome := waitForLoadBelowThreshold(context.Background())
	if outcome != "proceeded_immediately" {
		t.Fatalf("outcome = %q, want proceeded_immediately", outcome)
	}
	if delta := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_immediately")) - before; delta != 1 {
		t.Fatalf("proceeded_immediately counter: want +1, got +%v", delta)
	}
}

// TestLoadGuard_ProceedsAfterWait: fake above then below after N polls (poll
// interval overridden to 1ms) → {outcome="proceeded_after_wait"} +1.
func TestLoadGuard_ProceedsAfterWait(t *testing.T) {
	var calls int
	reader := func() (float64, error) {
		calls++
		if calls <= 3 {
			return 100.0, nil // above threshold
		}
		return 0.1, nil // below threshold
	}
	restore := installFakeLoadGuard(t, reader, time.Millisecond, 10)
	defer restore()

	before := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_after_wait"))
	done := make(chan string, 1)
	go func() { done <- waitForLoadBelowThreshold(context.Background()) }()
	select {
	case outcome := <-done:
		if outcome != "proceeded_after_wait" {
			t.Fatalf("outcome = %q, want proceeded_after_wait", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForLoadBelowThreshold hung — did not proceed after load dropped")
	}
	if delta := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_after_wait")) - before; delta != 1 {
		t.Fatalf("proceeded_after_wait counter: want +1, got +%v", delta)
	}
}

// TestLoadGuard_ProceedsTimeout: fake always above + a tiny cap (waitSecs=0)
// → {outcome="proceeded_timeout"} +1, and it does NOT hang.
func TestLoadGuard_ProceedsTimeout(t *testing.T) {
	reader := func() (float64, error) { return 100.0, nil } // always above
	restore := installFakeLoadGuard(t, reader, time.Millisecond, 0)
	defer restore()

	done := make(chan string, 1)
	go func() { done <- waitForLoadBelowThreshold(context.Background()) }()
	select {
	case outcome := <-done:
		if outcome != "proceeded_timeout" {
			t.Fatalf("outcome = %q, want proceeded_timeout", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForLoadBelowThreshold hung — did not proceed after cap elapsed")
	}
}

// TestLoadGuard_ProceedsReadError: fake returns error → proceeds,
// {outcome="proceeded_read_error"} +1 (fail-open).
func TestLoadGuard_ProceedsReadError(t *testing.T) {
	restore := installFakeLoadGuard(t,
		func() (float64, error) { return 0, errors.New("open /proc/loadavg: no such file") },
		time.Millisecond, 10)
	defer restore()

	before := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_read_error"))
	outcome := waitForLoadBelowThreshold(context.Background())
	if outcome != "proceeded_read_error" {
		t.Fatalf("outcome = %q, want proceeded_read_error", outcome)
	}
	if delta := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_read_error")) - before; delta != 1 {
		t.Fatalf("proceeded_read_error counter: want +1, got +%v", delta)
	}
}

// TestLoadGuard_CounterTimeout asserts the timeout counter increments on the
// timeout exit path (isolated counter check, mirroring TestCrossLaneLock_CounterOutcomes).
func TestLoadGuard_CounterTimeout(t *testing.T) {
	reader := func() (float64, error) { return 100.0, nil }
	restore := installFakeLoadGuard(t, reader, time.Millisecond, 0)
	defer restore()

	before := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_timeout"))
	outcome := waitForLoadBelowThreshold(context.Background())
	if outcome != "proceeded_timeout" {
		t.Fatalf("outcome = %q, want proceeded_timeout", outcome)
	}
	if delta := testutil.ToFloat64(LoadDeferredTotal.WithLabelValues("proceeded_timeout")) - before; delta != 1 {
		t.Fatalf("proceeded_timeout counter: want +1, got +%v", delta)
	}
}

// TestLoadGuard_NonHeavySkipsGuard asserts a non-heavy build never calls the
// loadavg guard — cheap go/static deploys stay unblocked. RED-on-revert:
// disabling the processBuild wiring (making the guard run for ALL builds) fails
// this test because the reader WILL be called on a light build.
func TestLoadGuard_NonHeavySkipsGuard(t *testing.T) {
	var readerCalls int
	reader := func() (float64, error) {
		readerCalls++
		return 0.1, nil
	}
	restoreGuard := installFakeLoadGuard(t, reader, time.Millisecond, 10)
	defer restoreGuard()

	fl := newFakeLock(t, true)
	restoreLock := installFakeLock(t, fl)
	defer restoreLock()

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("ok"), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	q.processBuild(ctx, lightReq(), false)

	if readerCalls != 0 {
		t.Fatalf("non-heavy build touched the loadavg guard: readerCalls=%d (want 0)", readerCalls)
	}
}

// TestLoadGuard_GuardBeforeLock asserts the loadavg guard runs BEFORE
// acquireCrossLaneLock in processBuild — the guard must NOT hold the box-wide
// ci-lock while idle-waiting on load (that would stall the CI lane).
func TestLoadGuard_GuardBeforeLock(t *testing.T) {
	var mu sync.Mutex
	var guardCalled bool
	var lockBeforeGuard bool

	reader := func() (float64, error) {
		mu.Lock()
		guardCalled = true
		mu.Unlock()
		return 0.1, nil // below threshold → proceeds immediately
	}
	restoreGuard := installFakeLoadGuard(t, reader, time.Millisecond, 10)
	defer restoreGuard()

	fl := newFakeLock(t, true)
	origAcq := ciLockAcquirer
	origRel := ciLockReleaser
	ciLockAcquirer = func(_ context.Context, env []string) (bool, error) {
		mu.Lock()
		if !guardCalled {
			lockBeforeGuard = true
		}
		mu.Unlock()
		return fl.acquire(context.Background(), env)
	}
	ciLockReleaser = fl.release
	defer func() {
		ciLockAcquirer = origAcq
		ciLockReleaser = origRel
	}()

	restoreLock := installFakeLock(t, fl)
	defer restoreLock()
	// installFakeLock overwrote ciLockAcquirer/ciLockReleaser with fl.acquire/fl.release;
	// re-install our instrumented versions AFTER installFakeLock.
	ciLockAcquirer = func(_ context.Context, env []string) (bool, error) {
		mu.Lock()
		if !guardCalled {
			lockBeforeGuard = true
		}
		mu.Unlock()
		return fl.acquire(context.Background(), env)
	}
	ciLockReleaser = fl.release

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("ok"), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	q.processBuild(ctx, heavyReq(), true)

	if !guardCalled {
		t.Fatal("loadavg guard was NOT called for a heavy build")
	}
	if lockBeforeGuard {
		t.Fatal("cross-lane lock acquired BEFORE loadavg guard ran — guard must run first to avoid holding the lock while idle-waiting")
	}
}

// TestReadProcLoadavg_Parse: valid /proc/loadavg line → correct 1-min float;
// malformed → error. Tests the real parseLoadavgLine used by readProcLoadavg.
func TestReadProcLoadavg_Parse(t *testing.T) {
	// Valid line: "1min 5min 15min running/total last_pid"
	load, err := parseLoadavgLine([]byte("0.52 0.43 0.35 3/123 45678\n"))
	if err != nil {
		t.Fatalf("valid line returned error: %v", err)
	}
	if load != 0.52 {
		t.Fatalf("1-min load = %v, want 0.52", load)
	}

	// Valid line with extra whitespace.
	load, err = parseLoadavgLine([]byte("  1.23  2.34  3.45  10/200  999  \n"))
	if err != nil || load != 1.23 {
		t.Fatalf("whitespace-padded line: load=%v err=%v, want 1.23 nil", load, err)
	}

	// Malformed: empty.
	if _, err = parseLoadavgLine([]byte("")); err == nil {
		t.Fatal("empty input should return error")
	}

	// Malformed: non-numeric first field.
	if _, err = parseLoadavgLine([]byte("foo bar baz")); err == nil {
		t.Fatal("non-numeric first field should return error")
	}
}
