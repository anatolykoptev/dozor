// Package tempguard guards the deploy-path temp-file contract in tests.
// Every deploy temp artifact must be created under os.TempDir() so a TMPDIR
// redirect captures it — the original leak was hardcoded /tmp paths that
// bypassed redirection entirely and accumulated on the host. Must-clean
// artifacts (worktrees, the compose-override file) must be removed before
// the creating call returns; kept diagnostic dumps (*.log) are tolerated
// inside a test's temp dir because they are bounded by deploy.PruneTempLogs.
package tempguard

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Patterns are the deploy-owned temp-file families watched by the guard.
// deploy-* covers both the webhook worktrees (deploy-<sha>-<ts>) and every
// deploy-manual-* artifact (manual worktrees + outcome logs).
var Patterns = []string{
	"dozor-build-*",
	"dozor-up-*",
	"dozor-compose-override-*",
	"deploy-manual-*",
	"deploy-*",
}

// staleWorkDirAge is how old a dozor-test-tmp-* suite dir must be before the
// next suite run sweeps it. A panic, `go test -timeout` kill, or SIGINT skips
// the RemoveAll at the end of RunWithGuard, so dead trees would otherwise
// accumulate in the ambient temp dir — the same leak class this guard exists
// to prevent, recreated by the guard itself.
const staleWorkDirAge = 6 * time.Hour

// watchedLeftovers returns the basenames of must-clean deploy artifacts still
// in dir: entries matching Patterns minus tolerated *.log dumps (those are
// bounded by deploy.PruneTempLogs).
func watchedLeftovers(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") || !isWatched(e.Name()) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// sweepStaleWorkDirs removes dozor-test-tmp-* suite dirs from the ambient
// temp dir that are older than staleWorkDirAge and owned by the current uid.
// Best-effort: ownership is checked via the file's uid (never another user's
// tree), and removal failures are ignored — the next suite run retries.
func sweepStaleWorkDirs() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "dozor-test-tmp-*"))
	if err != nil {
		return
	}
	uid := os.Getuid()
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || time.Since(fi.ModTime()) < staleWorkDirAge {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			continue // never touch another user's tree
		}
		_ = os.RemoveAll(m)
	}
}

// RunWithGuard redirects TMPDIR into a throwaway work dir for the whole
// suite, runs m, then fails the run if the suite left must-clean deploy
// artifacts inside that dir (worktree dirs, override files; kept *.log dumps
// are tolerated — they are bounded by deploy.PruneTempLogs). The assertion
// only ever covers the suite's OWN isolated dir: ambient /tmp is shared with
// other dozor checkouts on the same host, so diffing it cannot tell which
// process wrote an entry and flakes on a busy box. The hardcoded-/tmp leak
// class is covered deterministically instead — by the source scan for "/tmp
// literals in tmp_leak_guard_test.go. Returns the exit code for os.Exit.
func RunWithGuard(m *testing.M) int {
	sweepStaleWorkDirs()

	work, err := os.MkdirTemp("", "dozor-test-tmp-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tempguard: cannot create work dir: %v\n", err)
		return 1
	}
	if err := os.Setenv("TMPDIR", work); err != nil {
		fmt.Fprintf(os.Stderr, "tempguard: cannot redirect TMPDIR: %v\n", err)
		return 1
	}

	code := m.Run()

	leftover, lerr := watchedLeftovers(work)
	switch {
	case lerr != nil:
		fmt.Fprintf(os.Stderr, "tempguard: cannot list work dir %s: %v\n", work, lerr)
		if code == 0 {
			code = 1
		}
	case len(leftover) > 0:
		fmt.Fprintf(os.Stderr, "tempguard: suite left %d deploy temp entries in %s:\n  %s\n",
			len(leftover), work, strings.Join(leftover, "\n  "))
		if code == 0 {
			code = 1
		}
	}
	if err := os.RemoveAll(work); err != nil {
		fmt.Fprintf(os.Stderr, "tempguard: cannot remove work dir %s: %v\n", work, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// Isolate points the test's temp root at a fresh per-test dir and registers
// a cleanup asserting the test left behind no must-clean deploy artifact:
// kept *.log dumps are tolerated (bounded by deploy.PruneTempLogs), anything
// else matching Patterns — worktree dirs, override files — is a leak.
func Isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Cleanup(func() {
		leftover, err := watchedLeftovers(dir)
		if err != nil {
			t.Errorf("tempguard: cannot list %s: %v", dir, err)
			return
		}
		if len(leftover) > 0 {
			t.Errorf("tempguard: deploy temp artifacts left behind: %s", strings.Join(leftover, ", "))
		}
	})
}

func isWatched(name string) bool {
	for _, p := range Patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}
