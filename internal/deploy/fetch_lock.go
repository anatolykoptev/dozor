package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrFetchLock is returned when the per-directory fetch lock could not be
// acquired — another fetcher held it past the wait ceiling, or the caller's
// context expired before a slot freed. It is distinct from a fetch/git command
// failure: the guarded operation never ran. Callers that classify outcomes
// (syncOffBranch) test errors.Is(err, ErrFetchLock) so lock contention is
// surfaced as a real error rather than misfiled as a benign skip (e.g.
// syncDiverged, which means "the operator has local commits we must not
// clobber" — contention means nothing of the sort).
var ErrFetchLock = errors.New("fetch lock acquisition failed")

// fetchLockTimeout bounds how long a fetch waits for another concurrent fetch
// on the same clone to finish. A fetch that blocks forever is a worse failure
// than the ref race being fixed (issue #182).
//
// The ceiling is deliberately generous. Waiting costs latency; giving up costs
// the deploy, which is the very outcome this lock exists to prevent, so the two
// sides of the trade are not symmetric. A cold fetch of a large clone on a
// loaded box can legitimately outrun a tight bound. This is only an upper
// bound: the caller's context deadline still applies and wins when it is
// shorter.
//
// It is a var (not a const) so the own-deadline branch of acquireFetchLockInternal
// can be exercised in tests without a 5-minute wait; production code never
// mutates it. Note: source_sync's two call sites (defaultGitRefFFRunner and
// the up-front gitFetchRunner) run under a 60s sourceSyncTimeout, so this
// 5-minute ceiling never applies there — the caller's deadline always wins.
// The 5-minute ceiling is real for the other three sites (gitPrepare's two
// fetches and gitManualFetchRunner), which run without a shorter caller
// deadline.
var fetchLockTimeout = 5 * time.Minute

// withFetchLock serialises git fetch operations per target directory across
// processes. An in-process mutex is NOT sufficient — devin-run and operators
// fetch the same clones from outside this process (issue #182).
//
// The lock file lives at <git-common-dir>/dozor-fetch.lock, where the common
// dir is the resolved git directory (handles .git-as-a-file worktree layouts
// by resolving to the shared common dir, so two worktrees of the same main
// repo serialise against each other).
//
// The lock is held for the fetch ONLY — never extended over the build
// (heavySem(1) serialises deploys where that is wanted, and widening this lock
// would deadlock against it). The lock is released on every path including
// error and panic (deferred close/unlock).
//
// If the lock infrastructure itself fails (cannot resolve git dir, cannot open
// lock file), the fetch proceeds unlocked — the lock is a defence against a
// race, not a correctness precondition, and failing the fetch because of a lock
// file problem would be a regression. A timeout (another fetcher holds the lock
// too long) returns an error — a stuck fetch is a real problem worth surfacing.
//
// Cross-user limit: the lock file is created 0o600 (owner read/write only), so
// an operator running `git fetch` as a DIFFERENT user than the one dozor runs
// as cannot open it — acquireFetchLockInternal fail-opens and the two do not
// serialise. The lock serialises same-user fetchers only; cross-user fetching
// remains racy by design (widening the mode is a security tradeoff not asked
// for here).
func withFetchLock(ctx context.Context, dir string, fn func() error) error {
	f, err := acquireFetchLockInternal(ctx, dir)
	if err != nil {
		return err
	}
	if f == nil {
		// Lock infrastructure failure — proceed unlocked (fail-safe).
		return fn()
	}
	defer f.Close() // releases the flock on close
	return fn()
}

// resolveGitCommonDir resolves the git common directory for the given working
// tree path. For a standard clone (.git is a directory), the common dir is .git
// itself. For a worktree (.git is a file pointing to an admin dir), the common
// dir is resolved via the admin dir's commondir file — the shared directory
// where refs and objects live, so two worktrees of the same main repo share a
// lock.
func resolveGitCommonDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	// Symlinks are deliberately NOT resolved here. flock(2) keys on the inode,
	// and the kernel resolves symlinks when the lock file is opened, so two
	// different path spellings of one clone already contend — verified by
	// TestFetchLock_SymlinkedPathSharesLock, which still passes with any
	// canonicalisation removed. Calling filepath.EvalSymlinks would add a
	// syscall and a new fail-open branch to fix a race that does not exist.
	gitPath := filepath.Join(abs, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("stat .git in %s: %w", abs, err)
	}
	if info.IsDir() {
		return gitPath, nil
	}
	// .git is a file (worktree gitdir pointer): "gitdir: <admin-dir>"
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("read .git file: %w", err)
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf(".git file has no gitdir pointer: %q", line)
	}
	adminDir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(adminDir) {
		adminDir = filepath.Join(abs, adminDir)
	}
	adminDir = filepath.Clean(adminDir)
	// The admin dir's commondir file points to the shared common dir (relative
	// to adminDir). For a main repo's worktrees, this is "../.." → the main .git.
	commonFile := filepath.Join(adminDir, "commondir")
	commonRel, err := os.ReadFile(commonFile)
	if err != nil {
		// No commondir file — adminDir is itself the common dir.
		return adminDir, nil
	}
	commonDir := strings.TrimSpace(string(commonRel))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(adminDir, commonDir)
	}
	return filepath.Clean(commonDir), nil
}

// acquireFetchLockInternal acquires the fetch lock for dir and returns the
// open lock file (caller closes to release) or an error.
func acquireFetchLockInternal(ctx context.Context, dir string) (*os.File, error) {
	gitDir, err := resolveGitCommonDir(dir)
	if err != nil {
		slog.Warn("deploy: fetch lock — cannot resolve git common dir; proceeding unlocked",
			"dir", dir, "error", err)
		FetchLockFailOpenTotal.WithLabelValues("resolve_failure").Inc()
		return nil, nil
	}
	lockPath := filepath.Join(gitDir, "dozor-fetch.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600) //nolint:mnd // standard lock-file mode (owner read/write only)
	if err != nil {
		slog.Warn("deploy: fetch lock — cannot open lock file; proceeding unlocked",
			"path", lockPath, "error", err)
		FetchLockFailOpenTotal.WithLabelValues("open_failure").Inc()
		return nil, nil
	}
	timeout := fetchLockTimeout
	if dl, ok := ctx.Deadline(); ok {
		if t := time.Until(dl); t < timeout {
			timeout = t
		}
	}
	if timeout <= 0 {
		_ = f.Close()
		FetchLockTimeoutTotal.WithLabelValues("context").Inc()
		return nil, fmt.Errorf("%w: %s: context deadline already exceeded", ErrFetchLock, lockPath)
	}
	deadline := time.Now().Add(timeout)
	backoff := 50 * time.Millisecond
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil // acquired
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			FetchLockTimeoutTotal.WithLabelValues("deadline").Inc()
			return nil, fmt.Errorf("%w: %s: timed out after %s waiting for another fetch", ErrFetchLock, lockPath, timeout)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			FetchLockTimeoutTotal.WithLabelValues("context").Inc()
			return nil, fmt.Errorf("%w: %s: %w", ErrFetchLock, lockPath, ctx.Err())
		case <-time.After(backoff):
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}
