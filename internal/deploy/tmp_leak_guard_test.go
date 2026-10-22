package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/testutil/tempguard"
)

// The dozor-build-*.log dump is intentionally kept — its path is surfaced in
// the build error for the operator — but the family must stay bounded:
// runBuildWithFullLog prunes it to the newest MaxDeployTempLogs at write
// time. These dumps were the bulk of the /tmp accumulation.
//
// RED-on-revert: remove the PruneTempLogs call in runBuildWithFullLog — the
// seeded files are never pruned and the count assertion fails.
func TestRunBuildWithFullLog_PrunesOldDumps(t *testing.T) {
	tempguard.Isolate(t)
	seedOldLogs(t, "dozor-build-seed-", MaxDeployTempLogs+2)

	origBuild := buildRunner
	t.Cleanup(func() { buildRunner = origBuild })
	buildRunner = func(context.Context, string, []string) ([]byte, error) {
		return []byte("build boom"), errors.New("exit status 1")
	}

	errMsg := runBuildWithFullLog(context.Background(), BuildRequest{CommitSHA: "abc1234"}, nil)
	if errMsg == "" {
		t.Fatal("expected a build failure")
	}

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "dozor-build-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > MaxDeployTempLogs {
		t.Errorf("dozor-build-*.log not bounded: %d files, want <= %d (oldest must be pruned at write time)",
			len(matches), MaxDeployTempLogs)
	}
}

// Same bound for the up-phase dump family.
//
// RED-on-revert: remove the PruneTempLogs call in runUpWithFullLog.
func TestRunUpWithFullLog_PrunesOldDumps(t *testing.T) {
	tempguard.Isolate(t)
	seedOldLogs(t, "dozor-up-seed-", MaxDeployTempLogs+2)

	origUp := upRunner
	t.Cleanup(func() { upRunner = origUp })
	upRunner = func(context.Context, string, []string) ([]byte, error) {
		return []byte("up boom"), errors.New("exit status 1")
	}

	errMsg := runUpWithFullLog(context.Background(), BuildRequest{
		CommitSHA: "abc1234",
		Config:    RepoConfig{Services: []string{"svc"}},
	}, "deploy-abc")
	if errMsg == "" {
		t.Fatal("expected an up failure")
	}

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "dozor-up-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > MaxDeployTempLogs {
		t.Errorf("dozor-up-*.log not bounded: %d files, want <= %d (oldest must be pruned at write time)",
			len(matches), MaxDeployTempLogs)
	}
}

// ---------------------------------------------------------------------------
// Worktree cleanup gating: the deferred worktree remove in executeBuild and
// executeManualComposeDeploy had no failing test — replacing
// `defer worktreeCleanup()` (queue_build.go) or `defer cleanup()`
// (manual_deploy.go) with a discard kept every test green because git was
// stubbed and no real worktree ever existed. These tests run the REAL git
// binary against a real clone, so the worktree must be gone from both the
// filesystem and `git worktree list` when the call returns — on the success
// path and on a build failure.
//
// RED-on-revert: `_ = worktreeCleanup` / `_ = cleanup` in place of the defer —
// the worktree stays on disk and stays registered in `git worktree list`.
// ---------------------------------------------------------------------------

// realGitCmdRunner installs a cmdRunner that executes real git commands
// (`git worktree add` actually creates the worktree, so the cleanup defer is
// what actually removes it) while stubbing every non-git command (docker et
// al.). Returns a pointer to the worktree path captured from
// `git worktree add --detach <path> <target>`.
func realGitCmdRunner(t *testing.T) *string {
	t.Helper()
	wtPath := new(string)
	withCmdRunner(t, func(ctx context.Context, dir, name string, args ...string) error {
		if name != "git" {
			return nil
		}
		if len(args) >= 5 && args[0] == "worktree" && args[1] == "add" {
			*wtPath = args[len(args)-2]
		}
		return defaultRunCmd(ctx, dir, name, args...)
	})
	return wtPath
}

// composeConfigOutput stubs outputRunner's docker compose reads so a deploy
// runs end-to-end without real docker: the service builds with
// context=sourceDir (resolveBuildOverrides rebases it onto the worktree),
// reports no containers for the image snapshots, and shows a healthy running
// container with no declared ports for the post-up health check.
func composeConfigOutput(t *testing.T, svc, sourceDir string) {
	t.Helper()
	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "config":
			return []byte(`{"name":"proj","services":{"` + svc + `":{"image":"proj-` + svc + `:latest","build":{"context":"` + sourceDir + `"}}}}`), nil
		case "images":
			return []byte(`[]`), nil
		case "ps":
			return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
		}
		return nil, errors.New("unstubbed outputRunner call: " + strings.Join(args, " "))
	})
}

// assertWorktreeRemoved verifies the deploy worktree is gone — the directory
// removed and the registration dropped from `git worktree list`. wtPath is the
// path captured by realGitCmdRunner; empty means the deploy never created one.
func assertWorktreeRemoved(t *testing.T, repo, wtPath string) {
	t.Helper()
	if wtPath == "" {
		t.Fatal("git worktree add never ran — the deploy did not create a worktree")
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after the deploy returned (err=%v) — the cleanup defer did not run", wtPath, err)
	}
	if out := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, wtPath) {
		t.Errorf("git worktree list still registers %s after the deploy returned:\n%s", wtPath, out)
	}
}

func TestExecuteBuild_RemovesWorktree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr bool
	}{
		{"success", false},
		{"build_failure", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempguard.Isolate(t)
			defer zeroDelays(t)()
			_, clone, _ := newDeployCloneFixture(t)
			wtPath := realGitCmdRunner(t)
			composeConfigOutput(t, "svc", clone)
			buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
				if tc.wantErr {
					return []byte("build boom"), errors.New("exit status 1")
				}
				return nil, nil
			}

			ctx := context.Background()
			q := NewQueue(ctx, func(string) {})
			defer q.Close()

			req := makeReq("/fake/compose")
			req.Config.SourcePath = clone
			req.CommitSHA = gitOut(t, clone, "rev-parse", "HEAD")
			result := q.executeBuild(ctx, req)

			if tc.wantErr && result.Error == "" {
				t.Fatal("expected a build failure")
			}
			if !tc.wantErr && !result.Success {
				t.Fatalf("expected success, got: %s", result.Error)
			}
			assertWorktreeRemoved(t, clone, *wtPath)
		})
	}
}

func TestExecuteManualDeploy_RemovesWorktree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr bool
	}{
		{"success", false},
		{"build_failure", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempguard.Isolate(t)
			defer zeroDelays(t)()
			_, clone, _ := newDeployCloneFixture(t)
			wtPath := realGitCmdRunner(t)
			composeConfigOutput(t, "svc", clone)
			buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
				if tc.wantErr {
					return []byte("build boom"), errors.New("exit status 1")
				}
				return nil, nil
			}

			result := ExecuteManualDeploy(context.Background(), ManualDeployRequest{
				Repo: "test/manual-worktree-cleanup",
				Config: RepoConfig{
					Branch:      "main",
					SourcePath:  clone,
					ComposePath: "/fake/compose",
					Services:    []string{"svc"},
				},
			})

			if tc.wantErr && result.Error == "" {
				t.Fatal("expected a build failure")
			}
			if !tc.wantErr && !result.Success {
				t.Fatalf("expected success, got: %s", result.Error)
			}
			assertWorktreeRemoved(t, clone, *wtPath)
		})
	}
}

// TestNoHardcodedTmpPaths is the deterministic replacement for the removed
// ambient-/tmp snapshot diff in tempguard.RunWithGuard (which could not tell
// this suite's writes from another dozor checkout's on a shared host). A
// hard-coded "/tmp literal is the only way a deploy artifact escapes the
// TMPDIR redirect, so the guard scans the non-test .go sources of
// internal/deploy and internal/engine and fails on any that is not
// allowlisted with a reason.
func TestNoHardcodedTmpPaths(t *testing.T) {
	// literal (including the leading quote) → why a fixed host path is correct.
	allowed := map[string]string{
		`"/tmp/krolik-server-maintenance.lock"`: "cross-process ops lock shared with maintenance tooling — must be one fixed host path regardless of TMPDIR",
	}
	for _, dir := range []string{".", "../engine"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", filepath.Join(dir, name), err)
			}
			for ln, line := range strings.Split(string(data), "\n") {
				for rest := line; ; {
					idx := strings.Index(rest, `"/tmp`)
					if idx < 0 {
						break
					}
					literal := rest[idx:]
					if end := strings.IndexByte(literal[1:], '"'); end >= 0 {
						literal = literal[:end+2]
					}
					if _, ok := allowed[literal]; !ok {
						t.Errorf("%s:%d: hard-coded /tmp literal bypasses the TMPDIR redirect: %s",
							filepath.Join(dir, name), ln+1, strings.TrimSpace(line))
					}
					rest = rest[idx+len(literal):]
				}
			}
		}
	}
}

// seedOldLogs writes n matching files into the current temp dir with mtimes
// far in the past, so the prune sees them as the oldest of their family.
func seedOldLogs(t *testing.T, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := filepath.Join(os.TempDir(), fmt.Sprintf("%s%03d.log", prefix, i))
		if err := os.WriteFile(name, []byte("stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(i+1) * time.Hour)
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
}
