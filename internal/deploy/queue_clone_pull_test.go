package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helpers to swap injectable runners and restore on test exit.

func withGitStatus(t *testing.T, fn func(context.Context, string) ([]byte, error)) {
	t.Helper()
	orig := gitStatusRunner
	gitStatusRunner = fn
	t.Cleanup(func() { gitStatusRunner = orig })
}

func withGitFetch(t *testing.T, fn func(context.Context, string, string) error) {
	t.Helper()
	orig := gitFetchRunner
	gitFetchRunner = fn
	t.Cleanup(func() { gitFetchRunner = orig })
}

func withGitRevParse(t *testing.T, fn func(context.Context, string, string) (string, error)) {
	t.Helper()
	orig := gitRevParseRunner
	gitRevParseRunner = fn
	t.Cleanup(func() { gitRevParseRunner = orig })
}

func withGitPullFF(t *testing.T, fn func(context.Context, string, string) error) {
	t.Helper()
	orig := gitPullFFRunner
	gitPullFFRunner = fn
	t.Cleanup(func() { gitPullFFRunner = orig })
}

func withGitShortSHA(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := gitShortSHARunner
	gitShortSHARunner = fn
	t.Cleanup(func() { gitShortSHARunner = orig })
}

// TestPullDeployClone_EmptyPath is a no-op (no pull attempted).
func TestPullDeployClone_EmptyPath(t *testing.T) {
	// None of the runners should be called when clonePath is empty.
	called := false
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		called = true
		return nil, nil
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "", "main")
	if outcome != pullUpToDate {
		t.Errorf("expected up_to_date for empty path, got %q", outcome)
	}
	if called {
		t.Error("gitStatusRunner must not be called when clonePath is empty")
	}
}

// TestPullDeployClone_UpToDate — remote has nothing new.
func TestPullDeployClone_UpToDate(t *testing.T) {
	const sha = "abc1234"
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(""), nil // clean
	})
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		return sha, nil // FETCH_HEAD == HEAD
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		t.Error("gitPullFFRunner must not be called when already up-to-date")
		return nil
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main")
	if outcome != pullUpToDate {
		t.Errorf("expected up_to_date, got %q", outcome)
	}
}

// TestPullDeployClone_FastForward — remote has new commits; pull advances HEAD.
func TestPullDeployClone_FastForward(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(""), nil
	})
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	calls := 0
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		calls++
		switch ref {
		case "FETCH_HEAD":
			return "newsha1234", nil
		default: // HEAD
			if calls <= 2 { //nolint:mnd // first two calls are FETCH_HEAD + HEAD before pull
				return "oldsha0000", nil
			}
			return "newsha1234", nil // HEAD after pull
		}
	})
	pulled := false
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		pulled = true
		return nil
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main")
	if outcome != pullFastForward {
		t.Errorf("expected fast_forward, got %q", outcome)
	}
	if !pulled {
		t.Error("expected gitPullFFRunner to be called")
	}
}

// TestPullDeployClone_DirtySkipped — local edits prevent pull.
func TestPullDeployClone_DirtySkipped(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(" M docker-compose.yml\n"), nil // dirty
	})
	withGitFetch(t, func(_ context.Context, _, _ string) error {
		t.Error("gitFetchRunner must not be called on dirty tree")
		return nil
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main")
	if outcome != pullDirtySkipped {
		t.Errorf("expected dirty_skipped, got %q", outcome)
	}
}

// TestPullDeployClone_DivergedSkipped — ff-only fails (diverged history).
func TestPullDeployClone_DivergedSkipped(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(""), nil
	})
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		switch ref {
		case "FETCH_HEAD":
			return "remote999", nil
		default:
			return "local111", nil
		}
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		return errors.New("exit status 1: not possible to fast-forward, aborting")
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main")
	if outcome != pullDiverged {
		t.Errorf("expected diverged_skipped, got %q", outcome)
	}
}

// TestPullDeployClone_FetchError — git fetch fails; build proceeds with current state.
func TestPullDeployClone_FetchError(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(""), nil
	})
	withGitFetch(t, func(_ context.Context, _, _ string) error {
		return errors.New("could not resolve host: github.com")
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main")
	if outcome != pullError {
		t.Errorf("expected error, got %q", outcome)
	}
}

// TestPullDeployClone_GitStatusError — git status itself fails.
func TestPullDeployClone_GitStatusError(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("not a git repository")
	})

	outcome := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main")
	if outcome != pullError {
		t.Errorf("expected error, got %q", outcome)
	}
}

// TestPullDeployClone_DefaultBranchMain verifies that branch="" resolves to "main".
func TestPullDeployClone_DefaultBranchMain(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	var gotBranch string
	withGitFetch(t, func(_ context.Context, _, branch string) error {
		gotBranch = branch
		return nil
	})
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "abc", nil })

	pullDeployClone(context.Background(), "test/repo", "/fake/clone", "")
	if gotBranch != "main" {
		t.Errorf("expected branch=main, got %q", gotBranch)
	}
}

// TestResolveGitSHA_Success verifies the happy path.
func TestResolveGitSHA_Success(t *testing.T) {
	withGitShortSHA(t, func(_ context.Context, dir string) (string, error) {
		return "abc1234", nil
	})
	got := resolveGitSHA(context.Background(), "/some/dir")
	if got != "abc1234" {
		t.Errorf("expected abc1234, got %q", got)
	}
}

// TestResolveGitSHA_Empty — empty dir returns "unknown" without calling runner.
func TestResolveGitSHA_Empty(t *testing.T) {
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) {
		t.Error("runner must not be called on empty dir")
		return "", nil
	})
	if got := resolveGitSHA(context.Background(), ""); got != "unknown" {
		t.Errorf("expected unknown, got %q", got)
	}
}

// TestResolveGitSHA_Error — error falls back to "unknown".
func TestResolveGitSHA_Error(t *testing.T) {
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("not a git repo")
	})
	if got := resolveGitSHA(context.Background(), "/bad/dir"); got != "unknown" {
		t.Errorf("expected unknown on error, got %q", got)
	}
}

// TestComposeBuild_InjectsBuildArgs verifies that OXPULSE_GIT_SHA and
// BUILD_TIMESTAMP appear as --build-arg entries in the docker compose call.
func TestComposeBuild_InjectsBuildArgs(t *testing.T) {
	// Stub all git runners.
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "samesha", nil })
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) { return "deadbee", nil })

	// Stub outputRunner (used by resolveBuildOverrides).
	origOutput := outputRunner
	defer func() { outputRunner = origOutput }()
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		return []byte(`{"services":{"svc":{"build":{"context":"/fake/source"}}}}`), nil
	}

	var capturedArgs []string
	origBuild := buildRunner
	defer func() { buildRunner = origBuild }()
	buildRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		capturedArgs = args
		return nil, nil
	}

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "deadbeef",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{"svc"},
		},
	}

	errMsg := composeBuild(context.Background(), req, "/fake/worktree")
	if errMsg != "" {
		t.Fatalf("composeBuild: unexpected error: %s", errMsg)
	}

	args := strings.Join(capturedArgs, " ")
	if !strings.Contains(args, "--build-arg OXPULSE_GIT_SHA=deadbee") {
		t.Errorf("missing OXPULSE_GIT_SHA build-arg; got args: %s", args)
	}
	if !strings.Contains(args, "--build-arg BUILD_TIMESTAMP=") {
		t.Errorf("missing BUILD_TIMESTAMP build-arg; got args: %s", args)
	}

	// BUILD_TIMESTAMP must be a numeric unix epoch close to now.
	const marker = "--build-arg BUILD_TIMESTAMP="
	idx := strings.Index(args, marker)
	if idx < 0 {
		t.Fatalf("BUILD_TIMESTAMP marker not found in args: %s", args)
	}
	tsStr := strings.Fields(args[idx+len(marker):])[0]
	var ts int64
	if _, err := fmt.Sscanf(tsStr, "%d", &ts); err != nil {
		t.Fatalf("BUILD_TIMESTAMP %q is not an integer: %v", tsStr, err)
	}
	now := time.Now().Unix()
	if ts < now-10 || ts > now+10 { //nolint:mnd // 10s window
		t.Errorf("BUILD_TIMESTAMP %d is not within 10s of now (%d)", ts, now)
	}
}

// TestComposeBuild_InjectsBuildArgs_NoWorktree verifies that build-arg injection
// also works when worktreePath is empty (SourcePath is used for SHA resolution).
func TestComposeBuild_InjectsBuildArgs_NoWorktree(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "samesha", nil })
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) {
		return "fa11bac", nil
	})

	var capturedArgs []string
	origBuild := buildRunner
	defer func() { buildRunner = origBuild }()
	buildRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		capturedArgs = args
		return nil, nil
	}

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "fa11back",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{"svc"},
		},
	}

	// worktreePath = "" → no override generation
	errMsg := composeBuild(context.Background(), req, "")
	if errMsg != "" {
		t.Fatalf("composeBuild no-worktree: unexpected error: %s", errMsg)
	}

	args := strings.Join(capturedArgs, " ")
	if !strings.Contains(args, "--build-arg OXPULSE_GIT_SHA=fa11bac") {
		t.Errorf("missing OXPULSE_GIT_SHA in no-worktree case; got: %s", args)
	}
}

// TestPullDeployClone_Integration tests the full pull lifecycle against a real
// local git repo, exercising the default runner implementations end-to-end.
func TestPullDeployClone_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}

	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found, skipping integration test")
	}
	_ = git

	// Create a "remote" bare repo.
	remote := t.TempDir()
	mustRun(t, remote, "git", "init", "--bare", "--initial-branch=main")

	// Create the local clone.
	clone := t.TempDir()
	mustRun(t, clone, "git", "clone", remote, ".")
	mustRun(t, clone, "git", "config", "user.email", "test@test.com")
	mustRun(t, clone, "git", "config", "user.name", "Test")
	// initial commit so HEAD exists
	f := filepath.Join(clone, "docker-compose.yml")
	if err := os.WriteFile(f, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, clone, "git", "add", ".")
	mustRun(t, clone, "git", "commit", "-m", "init")
	mustRun(t, clone, "git", "push", "origin", "main")

	// Subtest A: already up-to-date.
	t.Run("up_to_date", func(t *testing.T) {
		outcome := pullDeployClone(context.Background(), "test/repo", clone, "main")
		if outcome != pullUpToDate {
			t.Errorf("expected up_to_date, got %q", outcome)
		}
	})

	// Push a new commit to remote so the clone can fast-forward.
	mustRun(t, remote, "git", "--bare", "commit-graph", "write") // harmless no-op, just ensures remote is valid

	// Create another worktree to push a second commit.
	pusher := t.TempDir()
	mustRun(t, pusher, "git", "clone", remote, ".")
	mustRun(t, pusher, "git", "config", "user.email", "test@test.com")
	mustRun(t, pusher, "git", "config", "user.name", "Test")
	f2 := filepath.Join(pusher, "docker-compose.yml")
	if err := os.WriteFile(f2, []byte("version: '3'\nservices: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, pusher, "git", "add", ".")
	mustRun(t, pusher, "git", "commit", "-m", "add service")
	mustRun(t, pusher, "git", "push", "origin", "main")

	// Subtest B: fast-forward.
	t.Run("fast_forward", func(t *testing.T) {
		outcome := pullDeployClone(context.Background(), "test/repo", clone, "main")
		if outcome != pullFastForward {
			t.Errorf("expected fast_forward, got %q", outcome)
		}
	})

	// Subtest C: dirty — create an untracked modification.
	t.Run("dirty_skipped", func(t *testing.T) {
		dirt := filepath.Join(clone, "docker-compose.yml")
		if err := os.WriteFile(dirt, []byte("DIRTY\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			mustRun(t, clone, "git", "checkout", "--", "docker-compose.yml")
		})
		outcome := pullDeployClone(context.Background(), "test/repo", clone, "main")
		if outcome != pullDirtySkipped {
			t.Errorf("expected dirty_skipped, got %q", outcome)
		}
	})
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // test helper, trusted args
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %q %v failed in %s: %v\n%s", name, args, dir, err, out)
	}
}
