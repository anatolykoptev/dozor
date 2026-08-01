package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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
const fetchLockTimeout = 5 * time.Minute

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
		return nil, nil
	}
	lockPath := filepath.Join(gitDir, "dozor-fetch.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600) //nolint:mnd // standard lock-file mode (owner read/write only)
	if err != nil {
		slog.Warn("deploy: fetch lock — cannot open lock file; proceeding unlocked",
			"path", lockPath, "error", err)
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
		return nil, fmt.Errorf("fetch lock %s: context deadline already exceeded", lockPath)
	}
	deadline := time.Now().Add(timeout)
	backoff := 50 * time.Millisecond
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil // acquired
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("fetch lock %s: timed out after %s waiting for another fetch", lockPath, timeout)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("fetch lock %s: %w", lockPath, ctx.Err())
		case <-time.After(backoff):
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}
