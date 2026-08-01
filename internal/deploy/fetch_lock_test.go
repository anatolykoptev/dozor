package deploy

import (
	"bytes"
	"context"
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

// TestFetchLock_TimeoutReturnsError asserts that a bounded wait is enforced: if
// another process holds the lock, a second acquire returns a clear error after
// the timeout rather than blocking forever.
func TestFetchLock_TimeoutReturnsError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}

	clone := setupFetchRaceFixture(t)

	// Goroutine 1: hold the lock until released.
	acquired := make(chan struct{})
	release := make(chan struct{})
	holdErr := make(chan error, 1)
	go func() {
		holdErr <- withFetchLock(context.Background(), clone, func() error {
			close(acquired) // signal: lock is held
			<-release
			return nil
		})
	}()
	<-acquired // wait for goroutine 1 to actually hold the lock

	// Goroutine 2: try to acquire with a 500ms timeout — must fail.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := withFetchLock(ctx, clone, func() error {
		t.Error("fn should not be called when the lock times out")
		return nil
	})
	if err == nil {
		t.Fatal("expected timeout error when the lock is held by another fetcher, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected a timeout/deadline error, got: %v", err)
	}

	// Release goroutine 1 and verify it completes.
	close(release)
	if err := <-holdErr; err != nil {
		t.Fatalf("holder returned error: %v", err)
	}
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
	acquired := make(chan struct{})
	release := make(chan struct{})
	holdErr := make(chan error, 1)
	go func() {
		holdErr <- withFetchLock(context.Background(), wtPath, func() error {
			close(acquired) // signal: lock is held
			<-release
			return nil
		})
	}()
	<-acquired // wait for the worktree holder to actually hold the lock

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = withFetchLock(ctx, mainRepo, func() error {
		t.Error("fn should not be called when the lock is held via the worktree")
		return nil
	})
	if err == nil {
		t.Fatal("expected timeout when main clone contends with worktree lock, got nil — lock not shared")
	}

	close(release)
	if err := <-holdErr; err != nil {
		t.Fatalf("worktree holder returned error: %v", err)
	}
}
