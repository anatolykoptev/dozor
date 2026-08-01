package deploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupFetchRaceFixture creates a bare remote, a clone whose origin/main lags
// behind the remote, and returns the clone path. The remote has a second commit
// (with a large file to widen the ref-update race window) that the clone has not
// fetched yet.
func setupFetchRaceFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}

	remote := t.TempDir()
	mustRun(t, remote, "git", "init", "--bare", "--initial-branch=main")

	// Seed the remote with an initial commit.
	seed := t.TempDir()
	mustRun(t, seed, "git", "clone", remote, ".")
	mustRun(t, seed, "git", "config", "user.email", "test@test.com")
	mustRun(t, seed, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, seed, "git", "add", ".")
	mustRun(t, seed, "git", "commit", "-m", "v1")
	mustRun(t, seed, "git", "push", "origin", "main")

	// Clone.
	clone := t.TempDir()
	mustRun(t, clone, "git", "clone", remote, ".")
	mustRun(t, clone, "git", "config", "user.email", "test@test.com")
	mustRun(t, clone, "git", "config", "user.name", "Test")

	// Add a second commit with a large file to widen the race window.
	big := bytes.Repeat([]byte("x"), 2<<20) //nolint:mnd // 2 MB to widen the ref-update race window
	if err := os.WriteFile(filepath.Join(seed, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, seed, "git", "add", ".")
	mustRun(t, seed, "git", "commit", "-m", "v2: add big file")
	mustRun(t, seed, "git", "push", "origin", "main")

	return clone
}

// TestFetchLock_SerialisesConcurrentFetches is the load-bearing test for issue
// #182: N concurrent git fetches against one clone where, without serialisation,
// all but the first lose the ref race ("cannot lock ref: is at X but expected
// Y"). With the per-directory file lock, fetches are serialised and all succeed.
//
// This test calls defaultGitFetchRunner — the production fetch function. Before
// the lock is applied, the ref race makes some fetches fail (RED). After the
// lock, all succeed (GREEN).
func TestFetchLock_SerialisesConcurrentFetches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	clone := setupFetchRaceFixture(t)

	const N = 20 //nolint:mnd // 20 concurrent fetchers to reliably reproduce the ref race
	errs := make([]error, N)
	var ready sync.WaitGroup
	ready.Add(N)
	var done sync.WaitGroup
	done.Add(N)
	start := make(chan struct{})

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer done.Done()
			ready.Done()
			<-start // barrier: all fetchers start simultaneously
			errs[idx] = defaultGitFetchRunner(context.Background(), clone, "main")
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()

	failed := 0
	var firstErr error
	for _, err := range errs {
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if failed > 0 {
		t.Fatalf("expected all %d concurrent fetches to succeed with the lock, but %d failed; first error: %v",
			N, failed, firstErr)
	}
}

// holdFetchLock acquires the fetch lock on clone in a background goroutine and
// holds it until the returned release func is called or the test ends. Uses
// t.Cleanup so the holder never leaks even if a t.Fatal fires before release
// (FIX 6: the old tests held the lock on <-release and leaked on assertion
// failure). The release func is idempotent (safe to call from t.Cleanup and
// manually).
func holdFetchLock(t *testing.T, clone string) (release func()) {
	t.Helper()
	acquired := make(chan struct{})
	rel := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(rel) }) }
	t.Cleanup(release)
	go func() {
		_ = withFetchLock(context.Background(), clone, func() error {
			close(acquired)
			<-rel
			return nil
		})
	}()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("holdFetchLock: lock never acquired within 5s")
	}
	return release
}

// TestFetchLock_OwnDeadlineTimesOut pins the OWN-DEADLINE branch of
// acquireFetchLockInternal's wait loop: when the caller's context has NO
// deadline, the lock's internal deadline is the binding one and the loop exits
// via `time.Now().After(deadline)` with the "timed out after <d>" message.
// The old TestFetchLock_TimeoutReturnsError used a 500ms ctx against a 50ms
// backoff so either the own-deadline or the ctx.Done branch could fire and it
// accepted both — a bug in either branch passed. This test pins exactly the
// own-deadline branch by using a deadline-less ctx and a shortened
// fetchLockTimeout (a var for exactly this testability).
func TestFetchLock_OwnDeadlineTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}
	clone := setupFetchRaceFixture(t)
	release := holdFetchLock(t, clone)

	// Shorten the lock's own deadline so the test doesn't wait 5 minutes. The
	// ctx has NO deadline, so the own deadline is the only one that can fire.
	orig := fetchLockTimeout
	fetchLockTimeout = 100 * time.Millisecond
	t.Cleanup(func() { fetchLockTimeout = orig })

	err := withFetchLock(context.Background(), clone, func() error {
		t.Error("fn must not be called when the lock's own deadline fires")
		return nil
	})
	if err == nil {
		t.Fatal("expected own-deadline timeout error, got nil")
	}
	if !errors.Is(err, ErrFetchLock) {
		t.Fatalf("expected errors.Is ErrFetchLock, got: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("expected 'timed out after' message (own-deadline branch), got: %v", err)
	}
	release()
}

// TestFetchLock_ContextCancelTimesOut pins the CTX.DONE branch of
// acquireFetchLockInternal's wait loop: when the caller's context is cancelled
// (with no deadline, so the lock's own 5-min deadline never fires), the loop
// exits via `<-ctx.Done()` wrapping ctx.Err(). This is the other branch the old
// test could not distinguish from the own-deadline branch.
func TestFetchLock_ContextCancelTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}
	clone := setupFetchRaceFixture(t)
	release := holdFetchLock(t, clone)

	// Cancellable ctx with NO deadline — the lock's own 5-min deadline is far
	// away, so only ctx.Done can fire. Cancel after a short delay.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := withFetchLock(ctx, clone, func() error {
		t.Error("fn must not be called when ctx is cancelled while waiting for the lock")
		return nil
	})
	if err == nil {
		t.Fatal("expected ctx-cancel error, got nil")
	}
	if !errors.Is(err, ErrFetchLock) {
		t.Fatalf("expected errors.Is ErrFetchLock, got: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected errors.Is context.Canceled (ctx.Done branch), got: %v", err)
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected 'context canceled' message (ctx.Done branch), got: %v", err)
	}
	release()
}

// TestFetchLock_WorktreeSharesLockWithMain asserts the .git-is-a-file
// (worktree) layout: the lock file resolves to the main repo's common dir, so
// a fetch from a worktree and a fetch from the main clone serialise against the
// same lock.
func TestFetchLock_WorktreeSharesLockWithMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}

	// Create a main repo with a commit.
	mainRepo := t.TempDir()
	mustRun(t, mainRepo, "git", "init", "--initial-branch=main")
	mustRun(t, mainRepo, "git", "config", "user.email", "test@test.com")
	mustRun(t, mainRepo, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(mainRepo, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, mainRepo, "git", "add", ".")
	mustRun(t, mainRepo, "git", "commit", "-m", "v1")

	// Create a worktree.
	wtPath := t.TempDir()
	mustRun(t, mainRepo, "git", "worktree", "add", "--detach", wtPath, "HEAD")

	// Verify resolveGitCommonDir resolves the worktree to the main repo's .git.
	mainGitDir := filepath.Join(mainRepo, ".git")
	resolved, err := resolveGitCommonDir(wtPath)
	if err != nil {
		t.Fatalf("resolveGitCommonDir(worktree) failed: %v", err)
	}
	if resolved != mainGitDir {
		t.Fatalf("worktree common dir = %q, want %q (main repo .git)", resolved, mainGitDir)
	}

	// Hold the lock from the worktree path; the main clone path must contend.
	// holdFetchLock uses t.Cleanup so the holder is always released (FIX 6).
	release := holdFetchLock(t, wtPath)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = withFetchLock(ctx, mainRepo, func() error {
		t.Error("fn should not be called when the lock is held via the worktree")
		return nil
	})
	if err == nil {
		t.Fatal("expected timeout when main clone contends with worktree lock, got nil — lock not shared")
	}
	if !errors.Is(err, ErrFetchLock) {
		t.Fatalf("expected ErrFetchLock, got: %v", err)
	}
	release()
}

// TestFetchLock_SymlinkedPathSharesLock (FIX 4): filepath.Abs does NOT resolve
// symlinks, so two symlink paths to one clone would produce different lock-file
// paths and silently not contend — a failure of the whole mechanism.
// resolveGitCommonDir must EvalSymlinks so both paths resolve to the same .git
// and therefore the same lock file.
func TestFetchLock_SymlinkedPathSharesLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}
	clone := setupFetchRaceFixture(t)

	// Create a symlink to the real clone in a sibling temp dir.
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "clone-symlink")
	if err := os.Symlink(clone, link); err != nil {
		t.Skipf("cannot create symlink (platform does not support it?): %v", err)
	}

	// Assert the PROPERTY (the two spellings contend), not the path strings
	// resolveGitCommonDir happens to return. The lock paths may legitimately
	// differ as text: flock(2) keys on the inode, and the kernel resolves the
	// symlink when the lock file is opened, so both spellings land on one file.
	// A string comparison here would pin the implementation instead - it goes
	// red when canonicalisation is removed even though contention still works.
	//
	// Hold the lock via the symlink; the real path must contend.
	release := holdFetchLock(t, link)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := withFetchLock(ctx, clone, func() error {
		t.Error("fn must not be called when the lock is held via a symlinked path")
		return nil
	})
	if err == nil {
		t.Fatal("expected timeout when real path contends with symlink-held lock, got nil — symlink not sharing lock")
	}
	if !errors.Is(err, ErrFetchLock) {
		t.Fatalf("expected ErrFetchLock, got: %v", err)
	}
	release()
}

// TestFetchLock_ProductionSitesBlockOnHeldLock (FIX 2): only
// defaultGitFetchRunner was driven through a real lock by a test. The other
// production fetch functions (gitPrepare, gitManualFetchRunner,
// defaultGitRefFFRunner) were untested against the lock — the structural reason
// FIX 1 existed (the one wrapping with any subtlety was the one no test
// covered). This table test asserts the PROPERTY, not the implementation: with
// the lock already held on a real temp clone, each production fetch function
// must block and then fail (ErrFetchLock) rather than proceeding immediately.
// gitPrepare does more than fetch — assert the observable boundary (a non-empty
// error message mentioning fetch, and no worktree created) rather than
// contorting it.
//
// RED-on-revert: remove the withFetchLock wrapper from the site a case covers
// — the underlying git command runs unserialised and succeeds in milliseconds,
// so the case returns in well under the lock timeout with no error.
func TestFetchLock_ProductionSitesBlockOnHeldLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}

	// Shorten the lock's own deadline so each case fails fast. Use a ctx with
	// NO deadline so the lock's OWN deadline is the binding one — proving the
	// lock blocks the site, not the ctx. Restored via t.Cleanup.
	orig := fetchLockTimeout
	fetchLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { fetchLockTimeout = orig })

	cases := []struct {
		name string
		// run returns an error; for gitPrepare (which returns an errMsg string)
		// the wrapper converts a non-empty message into an error.
		run func(ctx context.Context, clone string) error
	}{
		{
			name: "gitPrepare_fetch_origin",
			run: func(ctx context.Context, clone string) error {
				wtPath, _, cleanup, msg := gitPrepare(ctx, clone, "")
				if cleanup != nil {
					cleanup()
				}
				if wtPath != "" {
					t.Errorf("gitPrepare must NOT create a worktree when the fetch lock blocks it; got %q", wtPath)
				}
				if msg == "" {
					return nil
				}
				return errors.New(msg)
			},
		},
		{
			name: "gitManualFetchRunner",
			run: func(ctx context.Context, clone string) error {
				return gitManualFetchRunner(ctx, clone, "main")
			},
		},
		{
			name: "defaultGitRefFFRunner",
			run: func(ctx context.Context, clone string) error {
				out, err := defaultGitRefFFRunner(ctx, clone, "main")
				if err != nil && out != "" {
					t.Errorf("defaultGitRefFFRunner: expected empty out on lock failure (cosmetic fix), got %q", out)
				}
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := setupFetchRaceFixture(t)
			release := holdFetchLock(t, clone)

			ctx := context.Background() // no deadline — the lock's own (shortened) deadline fires
			start := time.Now()
			err := tc.run(ctx, clone)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatalf("%s: expected ErrFetchLock (lock must block the site), got nil — site proceeded without the lock", tc.name)
			}
			// gitPrepare wraps the lock error into a string message, so it does
			// not satisfy errors.Is(ErrFetchLock); assert by message instead. The
			// other two return the error directly.
			if tc.name == "gitPrepare_fetch_origin" {
				if !strings.Contains(err.Error(), "fetch") {
					t.Fatalf("%s: error must mention fetch, got: %v", tc.name, err)
				}
			} else if !errors.Is(err, ErrFetchLock) {
				t.Fatalf("%s: expected errors.Is ErrFetchLock, got: %v", tc.name, err)
			}
			// Must have actually blocked ~the lock timeout, not returned
			// immediately — proving it waited on the held lock rather than
			// proceeding.
			//
			// This elapsed-time guard is what catches an amputated wrapper for
			// gitPrepare and gitManualFetchRunner, whose fetches succeed in
			// milliseconds without the lock. It does NOT carry the
			// defaultGitRefFFRunner case: unlocked, that self-fetch fails fast
			// with "refusing to fetch into branch 'refs/heads/main' checked out
			// at ..." rather than succeeding, so there it is the
			// errors.Is(ErrFetchLock) assertion above that goes red. Both cases
			// are gated — just by different assertions.
			if elapsed < 150*time.Millisecond {
				t.Errorf("%s: returned in %v — expected to block ~%v on the held lock (proceeded without the lock?)",
					tc.name, elapsed, fetchLockTimeout)
			}
			release()
		})
	}
}
