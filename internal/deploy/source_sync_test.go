package deploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- DI-swap helpers (mirror queue_clone_pull_test.go) ---

func withGitRefFF(t *testing.T, fn func(context.Context, string, string) (string, error)) {
	t.Helper()
	orig := gitRefFFRunner
	gitRefFFRunner = fn
	t.Cleanup(func() { gitRefFFRunner = orig })
}

func withGitIndexLock(t *testing.T, fn func(string) bool) {
	t.Helper()
	orig := gitIndexLockPresent
	gitIndexLockPresent = fn
	t.Cleanup(func() { gitIndexLockPresent = orig })
}

func withSourceSyncEnabled(t *testing.T, enabled bool) {
	t.Helper()
	orig := sourceSyncEnabled
	sourceSyncEnabled = enabled
	t.Cleanup(func() { sourceSyncEnabled = orig })
}

// mustGitRepoWithOriginMain creates a real git repo in a temp dir whose
// `origin/main` remote-tracking ref exists, so detectDefaultBranch (which is
// NOT a swappable seam — it shells out via runCmd) resolves to "main"
// deterministically. The fetch/revparse/pullFF/refFF runners stay swapped, so
// no network is touched. Skips the test if git is unavailable.
func mustGitRepoWithOriginMain(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping")
	}
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "--initial-branch=main", "-q")
	mustRun(t, dir, "git", "config", "user.email", "t@t.com")
	mustRun(t, dir, "git", "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "init", "-q")
	// Forge an origin/main remote-tracking ref pointing at HEAD so
	// `git rev-parse --verify origin/main` succeeds (→ detectDefaultBranch="main").
	mustRun(t, dir, "git", "update-ref", "refs/remotes/origin/main", "HEAD")
	return dir
}

// ---------------------------------------------------------------------------
// FF-2: contract-equivalence / drift guard.
// Asserts syncSourceCheckout's decision table matches the established ff/dirty/
// lock contract shared with pullDeployClone and the timer, so the two impls
// cannot silently diverge. Each case uses the DI runner seams to inject fakes.
// ---------------------------------------------------------------------------

func TestSyncSourceCheckout_ContractMatrix(t *testing.T) {
	t.Run("flag_off_makes_zero_git_calls", func(t *testing.T) {
		withSourceSyncEnabled(t, false)
		called := false
		withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { called = true; return nil, nil })
		withGitIndexLock(t, func(string) bool { called = true; return false })

		got := syncSourceCheckout(context.Background(), "r", "/src", "/clone")
		if got != syncDisabled {
			t.Fatalf("flag off: got %q, want %q", got, syncDisabled)
		}
		if called {
			t.Error("flag off must short-circuit before ANY git call")
		}
	})

	t.Run("guard_source_equals_deploy_clone", func(t *testing.T) {
		withSourceSyncEnabled(t, true)
		called := false
		withGitIndexLock(t, func(string) bool { called = true; return false })

		got := syncSourceCheckout(context.Background(), "r", "/same", "/same")
		if got != syncUpToDate {
			t.Fatalf("source==clone guard: got %q, want %q (no double-pull)", got, syncUpToDate)
		}
		if called {
			t.Error("source==clone guard must skip before touching git")
		}
	})

	t.Run("empty_source_path", func(t *testing.T) {
		withSourceSyncEnabled(t, true)
		got := syncSourceCheckout(context.Background(), "r", "", "/clone")
		if got != syncUpToDate {
			t.Fatalf("empty source: got %q, want %q", got, syncUpToDate)
		}
	})

	t.Run("index_lock_present", func(t *testing.T) {
		withSourceSyncEnabled(t, true)
		withGitIndexLock(t, func(string) bool { return true })
		statusCalled := false
		withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { statusCalled = true; return nil, nil })

		got := syncSourceCheckout(context.Background(), "r", "/src", "/clone")
		if got != syncLockedSkipped {
			t.Fatalf("index.lock: got %q, want %q", got, syncLockedSkipped)
		}
		if statusCalled {
			t.Error("index.lock must skip before git status")
		}
	})

	t.Run("dirty_tracked_skips", func(t *testing.T) {
		withSourceSyncEnabled(t, true)
		withGitIndexLock(t, func(string) bool { return false })
		withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
			return []byte(" M src/main.go\n"), nil
		})
		fetched := false
		withGitFetch(t, func(_ context.Context, _, _ string) error { fetched = true; return nil })

		got := syncSourceCheckout(context.Background(), "r", "/src", "/clone")
		if got != syncDirtySkipped {
			t.Fatalf("dirty tracked: got %q, want %q", got, syncDirtySkipped)
		}
		if fetched {
			t.Error("dirty tracked must skip before fetch — never overwrite operator edits")
		}
	})

	t.Run("untracked_only_proceeds", func(t *testing.T) {
		// Untracked scratch (agent plans/reports) must NOT block — mirrors
		// classifyPorcelain's contract in pullDeployClone.
		dir := mustGitRepoWithOriginMain(t)
		withSourceSyncEnabled(t, true)
		withGitIndexLock(t, func(string) bool { return false })
		withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
			return []byte("?? plans/foo.md\n?? reports/bar.md\n"), nil
		})
		withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) { return "same", nil })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncUpToDate {
			t.Fatalf("untracked only: got %q, want %q (must proceed past dirty-check)", got, syncUpToDate)
		}
	})
}

// ---------------------------------------------------------------------------
// Behaviour matrix: on-branch ff/up-to-date and off-branch ref-ff / elsewhere.
// ---------------------------------------------------------------------------

func TestSyncSourceCheckout_OnBranch(t *testing.T) {
	dir := mustGitRepoWithOriginMain(t)
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return nil, nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	// Default the up-front fetch to a no-op; subtests override as needed.
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })

	t.Run("up_to_date", func(t *testing.T) {
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "same", nil })
		pulled := false
		withGitPullFF(t, func(_ context.Context, _, _ string) error { pulled = true; return nil })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncUpToDate {
			t.Fatalf("on-branch up-to-date: got %q, want %q", got, syncUpToDate)
		}
		if pulled {
			t.Error("must not ff-pull when FETCH_HEAD == HEAD")
		}
	})

	t.Run("ff_updated", func(t *testing.T) {
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		n := 0
		withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
			n++
			if ref == "FETCH_HEAD" {
				return "new", nil
			}
			// first HEAD = old, post-pull HEAD = new
			if n >= 3 {
				return "new", nil
			}
			return "old", nil
		})
		pulled := false
		withGitPullFF(t, func(_ context.Context, _, _ string) error { pulled = true; return nil })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncFFUpdated {
			t.Fatalf("on-branch ff: got %q, want %q", got, syncFFUpdated)
		}
		if !pulled {
			t.Error("expected ff-only pull to run")
		}
	})

	t.Run("ff_pull_refused_is_diverged", func(t *testing.T) {
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
			if ref == "FETCH_HEAD" {
				return "new", nil
			}
			return "old", nil
		})
		withGitPullFF(t, func(_ context.Context, _, _ string) error { return errors.New("not a fast-forward") })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncDiverged {
			t.Fatalf("on-branch ff-refuse (local commits ahead): got %q, want %q", got, syncDiverged)
		}
	})

	t.Run("upfront_fetch_error", func(t *testing.T) {
		// The single up-front `git fetch origin <branch>` failing is a real
		// error — the freshness step that both branches depend on did not run.
		withGitFetch(t, func(_ context.Context, _, _ string) error { return errors.New("network down") })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncError {
			t.Fatalf("up-front fetch error: got %q, want %q", got, syncError)
		}
	})
}

func TestSyncSourceCheckout_OffBranch(t *testing.T) {
	dir := mustGitRepoWithOriginMain(t)
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return nil, nil })
	// Checkout is on a FEATURE branch, not main.
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "feat/x", nil })
	// The single up-front fetch must NOT ff-pull the feature-branch worktree.
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		t.Error("off-branch must NEVER ff-pull the worktree (would touch the feature branch)")
		return nil
	})

	t.Run("upfront_fetch_then_ref_ff_freshness", func(t *testing.T) {
		// FRESHNESS GUARD (regression for the off-branch staleness class): the
		// off-branch path MUST run the real `git fetch origin <branch>` up front
		// so the local origin/<branch> ref is current before the self-fetch.
		// Without it the ref would advance only to a stale local ref.
		fetchedBranch := ""
		withGitFetch(t, func(_ context.Context, _, branch string) error { fetchedBranch = branch; return nil })
		refFFCalled := false
		withGitRefFF(t, func(_ context.Context, _, branch string) (string, error) {
			refFFCalled = true
			if branch != "main" {
				t.Errorf("ref-ff must target default branch main, got %q", branch)
			}
			return "", nil
		})
		// before != after → ff_updated
		n := 0
		withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) {
			n++
			if n == 1 {
				return "before", nil
			}
			return "after", nil
		})

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncFFUpdated {
			t.Fatalf("off-branch ref-ff: got %q, want %q", got, syncFFUpdated)
		}
		if fetchedBranch != "main" {
			t.Errorf("off-branch must fetch origin main up front for freshness, fetched %q", fetchedBranch)
		}
		if !refFFCalled {
			t.Error("off-branch must use the self-fetch ref-ff path")
		}
	})

	t.Run("checked_out_elsewhere_is_benign", func(t *testing.T) {
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		withGitRefFF(t, func(_ context.Context, _, _ string) (string, error) {
			return "fatal: refusing to fetch into branch 'refs/heads/main' checked out at '/other/wt'", errors.New("exit 128")
		})
		withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "x", nil })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncCheckedOutElsewhere {
			t.Fatalf("off-branch checked-out-elsewhere: got %q, want %q", got, syncCheckedOutElsewhere)
		}
	})

	t.Run("ref_ff_diverged_is_benign", func(t *testing.T) {
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		withGitRefFF(t, func(_ context.Context, _, _ string) (string, error) {
			return "fatal: rejected: non-fast-forward", errors.New("exit 1")
		})
		withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "x", nil })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncDiverged {
			t.Fatalf("off-branch diverged ref: got %q, want %q", got, syncDiverged)
		}
	})

	t.Run("ref_ff_no_op_is_up_to_date", func(t *testing.T) {
		withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
		withGitRefFF(t, func(_ context.Context, _, _ string) (string, error) { return "", nil })
		withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "same", nil })

		got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
		if got != syncUpToDate {
			t.Fatalf("off-branch no-op: got %q, want %q", got, syncUpToDate)
		}
	})
}

// TestSyncSourceCheckout_StatusErrorIsError — a git status failure surfaces as
// "error", not a silent pass.
func TestSyncSourceCheckout_StatusErrorIsError(t *testing.T) {
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("boom") })

	got := syncSourceCheckout(context.Background(), "r", "/src", "/clone")
	if got != syncError {
		t.Fatalf("status error: got %q, want %q", got, syncError)
	}
}

// ---------------------------------------------------------------------------
// FF-1: off-critical-path invariant. processBuild must return WITHOUT waiting
// for a slow syncSourceCheckout — the sync runs in a detached goroutine.
// A sync that blocks/errors does NOT affect the build result or block the
// queue worker; the metric records the outcome and the goroutine is reaped.
// ---------------------------------------------------------------------------

func TestProcessBuild_SourceSyncOffCriticalPath(t *testing.T) {
	defer zeroDelays(t)() // stubs buildRunner/upRunner to no-ops so executeBuild is fast
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })

	// A status runner that blocks for longer than processBuild should ever take —
	// simulates a slow/hung sync. If processBuild waited on the sync goroutine,
	// it would block here too.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var once sync.Once
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		once.Do(func() { entered <- struct{}{} })
		<-release // block until the test releases
		return nil, errors.New("released late")
	})
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	// SourcePath non-empty → the sync goroutine launches. DeployClonePath empty so
	// the sync's guard does NOT short-circuit (it must reach the blocking status
	// runner). Source==clone would skip; we use distinct paths.
	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: "/tmp",
			SourcePath:  "/nonexistent-src", // gitPrepare fails fast; build result irrelevant to FF-1
			Services:    []string{"svc"},
		},
	}

	start := time.Now()
	q.processBuild(ctx, req, false)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("processBuild blocked %v on the source sync — must be off the critical path", elapsed)
	}

	// Prove the sync goroutine actually launched (entered the blocking status
	// runner) — confirming the off-path goroutine ran independently of the
	// already-returned processBuild.
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("source sync goroutine never started")
	}
	close(release) // let the goroutine finish so the test doesn't leak it
}

// ---------------------------------------------------------------------------
// FF-3: metric registered with the documented label set.
// ---------------------------------------------------------------------------

func TestDeploySourceSyncTotal_Registered(t *testing.T) {
	// Mirrors the DeployClonePullTotal registration test pattern: a WithLabelValues
	// with the documented (repo,result) cardinality must not panic, and the
	// counter must be a registered promauto vec.
	if DeploySourceSyncTotal == nil {
		t.Fatal("DeploySourceSyncTotal is nil — not registered")
	}
	for _, res := range []sourceSyncOutcome{
		syncUpToDate, syncFFUpdated, syncDirtySkipped, syncLockedSkipped,
		syncDisabled, syncCheckedOutElsewhere, syncDiverged, syncError,
	} {
		// Must accept exactly (repo, result) — wrong arity panics.
		DeploySourceSyncTotal.WithLabelValues("test/repo", string(res)).Inc()
	}
	// The "panic" result string (emitted by the goroutine's recover) must also
	// be a valid label value.
	DeploySourceSyncTotal.WithLabelValues("test/repo", "panic").Inc()
}
