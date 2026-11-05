package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sentinelHarness swaps the REAL acquire/release seams back in, pointed at fake
// hook scripts, and records the pid of every sentinel the lock spawns.
type sentinelHarness struct {
	dir  string
	pids []int
}

func newSentinelHarness(t *testing.T, acquireBody, releaseBody string) *sentinelHarness {
	t.Helper()
	h := &sentinelHarness{dir: t.TempDir()}
	write := func(name, body string) string {
		p := filepath.Join(h.dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldAcq, oldRel := ciLockAcquireScript, ciLockReleaseScript
	oldAcquirer, oldReleaser, oldStarter := ciLockAcquirer, ciLockReleaser, leaseSentinelStarter
	t.Cleanup(func() {
		ciLockAcquireScript, ciLockReleaseScript = oldAcq, oldRel
		ciLockAcquirer, ciLockReleaser, leaseSentinelStarter = oldAcquirer, oldReleaser, oldStarter
	})
	ciLockAcquireScript = write("acquire.sh", acquireBody)
	ciLockReleaseScript = write("release.sh", releaseBody)
	ciLockAcquirer = defaultCILockAcquirer
	ciLockReleaser = defaultCILockReleaser
	leaseSentinelStarter = func() (int, func(), error) {
		pid, stop, err := startLeaseSentinel()
		h.pids = append(h.pids, pid)
		return pid, stop, err
	}
	return h
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lease sentinel pid %d still alive after the releaser ran — the lease would outlive the build", pid)
}

func (h *sentinelHarness) onlyPID(t *testing.T) int {
	t.Helper()
	if len(h.pids) != 1 {
		t.Fatalf("expected exactly one sentinel per build, got %v", h.pids)
	}
	return h.pids[0]
}

// The hook must receive the sentinel's pid — not nothing (daemon PPID fallback)
// — and the sentinel must be alive while the build holds the lock.
func TestLeaseSentinel_HookGetsSentinelPID_AliveDuringBuild(t *testing.T) {
	h := newSentinelHarness(t,
		`printf %s "$CI_LOCK_WORKER_PID" > "$0.pid"; kill -0 "$CI_LOCK_WORKER_PID"`,
		`exit 0`)
	release := acquireCrossLaneLock(context.Background(), "svc", "sha1")
	pid := h.onlyPID(t)
	got, err := os.ReadFile(ciLockAcquireScript + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strconv.Itoa(pid) {
		t.Fatalf("hook saw CI_LOCK_WORKER_PID=%q, sentinel pid is %d", got, pid)
	}
	if !pidAlive(pid) {
		t.Fatal("sentinel dead while the build still holds the lock")
	}
	release()
	waitDead(t, pid)
}

// Mutation (verified RED): delete the `defer stopSentinel()` line in the
// releaser closure of acquireCrossLaneLock (crosslane_lock.go) — every case
// below then fails in waitDead, because the sentinel outlives the build.
func TestLeaseSentinel_DiesWhenBuildEnds(t *testing.T) {
	cases := []struct {
		name    string
		acquire string
		release string
		run     func(t *testing.T, release func())
		ctxFn   func() (context.Context, context.CancelFunc)
	}{
		{
			name: "success", acquire: `exit 0`, release: `exit 0`,
			run: func(_ *testing.T, release func()) { release() },
		},
		{
			// The 2026-11 incident: the release script itself fails, so only
			// the sentinel kill bounds the lease.
			name: "release script fails", acquire: `exit 0`, release: `echo boom >&2; exit 1`,
			run: func(_ *testing.T, release func()) { release() },
		},
		{
			name: "acquire times out", acquire: `exit 1`, release: `exit 0`,
			run: func(_ *testing.T, release func()) { release() },
		},
		{
			name: "build panics", acquire: `exit 0`, release: `exit 0`,
			run: func(t *testing.T, release func()) {
				defer func() {
					if recover() == nil {
						t.Fatal("expected the simulated build panic")
					}
				}()
				defer release()
				panic("simulated build panic")
			},
		},
		{
			name: "ctx cancelled during acquire", acquire: `exec sleep 30`, release: `exit 0`,
			ctxFn: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(150*time.Millisecond, cancel)
				return ctx, cancel
			},
			run: func(_ *testing.T, release func()) { release() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSentinelHarness(t, tc.acquire, tc.release)
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tc.ctxFn != nil {
				ctx, cancel = tc.ctxFn()
			}
			defer cancel()
			release := acquireCrossLaneLock(ctx, "svc", "sha2")
			pid := h.onlyPID(t)
			tc.run(t, release)
			waitDead(t, pid)
		})
	}
}

// Calling the releaser twice (defer + explicit) must not panic or double-reap.
func TestLeaseSentinel_StopIdempotent(t *testing.T) {
	h := newSentinelHarness(t, `exit 0`, `exit 0`)
	release := acquireCrossLaneLock(context.Background(), "svc", "sha3")
	release()
	release()
	waitDead(t, h.onlyPID(t))
}

// A sentinel that cannot start must not block the build: the lock degrades to
// the hook's own binding and no CI_LOCK_WORKER_PID is passed.
func TestLeaseSentinel_StartFailureFallsBack(t *testing.T) {
	_ = newSentinelHarness(t, `printf %s "${CI_LOCK_WORKER_PID:-unset}" > "$0.pid"`, `exit 0`)
	leaseSentinelStarter = func() (int, func(), error) { return 0, nil, os.ErrPermission }
	release := acquireCrossLaneLock(context.Background(), "svc", "sha4")
	defer release()
	got, _ := os.ReadFile(ciLockAcquireScript + ".pid")
	if string(got) != "unset" {
		t.Fatalf("CI_LOCK_WORKER_PID must be absent on sentinel failure, hook saw %q", got)
	}
}
