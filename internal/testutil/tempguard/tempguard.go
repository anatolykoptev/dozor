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
	"testing"
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

// Entries returns the basenames inside dir matching Patterns.
func Entries(dir string) map[string]bool {
	out := map[string]bool{}
	for _, p := range Patterns {
		matches, _ := filepath.Glob(filepath.Join(dir, p))
		for _, m := range matches {
			out[filepath.Base(m)] = true
		}
	}
	return out
}

// NewEntries returns the matching paths present in dirs now but absent from
// before (a Snapshot taken earlier).
func NewEntries(before map[string]map[string]bool, dirs []string) []string {
	var out []string
	for _, d := range dirs {
		for name := range Entries(d) {
			if !before[d][name] {
				out = append(out, filepath.Join(d, name))
			}
		}
	}
	sort.Strings(out)
	return out
}

// Snapshot captures Entries(dir) for each dir.
func Snapshot(dirs []string) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(dirs))
	for _, d := range dirs {
		out[d] = Entries(d)
	}
	return out
}

// RunWithGuard snapshots the ambient temp dir — plus literal /tmp when the
// ambient dir is elsewhere — redirects TMPDIR into a throwaway work dir for
// the whole suite, runs m, and fails the run if the suite added any
// deploy-owned temp entry outside the sandbox. The literal-/tmp watch is
// what a reverted hardcoded path trips even when the ambient TMPDIR was
// already redirected. Returns the exit code to pass to os.Exit.
func RunWithGuard(m *testing.M) int {
	ambient := os.TempDir()
	watchDirs := []string{ambient}
	if ambient != "/tmp" {
		watchDirs = append(watchDirs, "/tmp")
	}
	before := Snapshot(watchDirs)

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

	if leaked := NewEntries(before, watchDirs); len(leaked) > 0 {
		fmt.Fprintf(os.Stderr, "tempguard: suite left %d deploy temp entries outside TMPDIR:\n  %s\n",
			len(leaked), strings.Join(leaked, "\n  "))
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("tempguard: cannot list %s: %v", dir, err)
			return
		}
		var leftover []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".log") || !isWatched(e.Name()) {
				continue
			}
			leftover = append(leftover, e.Name())
		}
		if len(leftover) > 0 {
			sort.Strings(leftover)
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
