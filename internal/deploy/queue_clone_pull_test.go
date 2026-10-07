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

	"github.com/prometheus/client_golang/prometheus/testutil"
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

func withGitCurrentBranch(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := gitCurrentBranchRunner
	gitCurrentBranchRunner = fn
	t.Cleanup(func() { gitCurrentBranchRunner = orig })
}

// ---------------------------------------------------------------------------
// Real-git deploy-clone guard tests (issue #239).
//
// These exercise the PRODUCTION git runners (no seam stubs) against a bare
// origin repo, a deploy clone, and a pusher clone under t.TempDir(). The
// contract is fail-closed: a dirty, diverged, or unfetchable deploy clone
// REFUSES the deploy — the compose config it serves must never silently lag
// origin/<branch> again (the ox-browser 401 incident).
// ---------------------------------------------------------------------------

func gitConfigUser(t *testing.T, dir string) {
	t.Helper()
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitOut runs a git command in dir and returns trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // test helper, trusted args
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pushCommit writes name=content in pusher, commits and pushes to origin main.
// Returns the pushed commit SHA.
func pushCommit(t *testing.T, pusher, name, content string) string {
	t.Helper()
	writeFile(t, filepath.Join(pusher, name), content)
	mustRun(t, pusher, "git", "add", ".")
	mustRun(t, pusher, "git", "commit", "-m", "update "+name)
	mustRun(t, pusher, "git", "push", "origin", "main")
	return gitOut(t, pusher, "rev-parse", "HEAD")
}

// newDeployCloneFixture builds a bare origin repo, a deploy clone, and a
// pusher clone — all real git repos under t.TempDir() — sharing one commit
// on main.
func newDeployCloneFixture(t *testing.T) (origin, clone, pusher string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH — skipping deploy-clone guard test")
	}
	origin = t.TempDir()
	mustRun(t, origin, "git", "init", "--bare", "--initial-branch=main")

	seed := t.TempDir()
	mustRun(t, seed, "git", "clone", origin, ".")
	gitConfigUser(t, seed)
	writeFile(t, filepath.Join(seed, "docker-compose.yml"), "version: '3'\n")
	mustRun(t, seed, "git", "add", ".")
	mustRun(t, seed, "git", "commit", "-m", "init")
	mustRun(t, seed, "git", "push", "origin", "main")

	clone = t.TempDir()
	mustRun(t, clone, "git", "clone", origin, ".")
	gitConfigUser(t, clone)

	pusher = t.TempDir()
	mustRun(t, pusher, "git", "clone", origin, ".")
	gitConfigUser(t, pusher)
	return origin, clone, pusher
}

// (a) A dirty tracked file refuses the deploy, names the file, and bumps
// dozor_deploy_clone_refused_total{reason="dirty"}.
func TestPullDeployClone_DirtyRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)
	writeFile(t, filepath.Join(clone, "docker-compose.yml"), "version: '3'\n# DIRTY local edit\n")

	const repo = "test/dirty-refused"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty"))

	errMsg := pullDeployClone(context.Background(), repo, clone, "main")
	if errMsg == "" {
		t.Fatal("dirty deploy clone must refuse the deploy, got no error")
	}
	if !strings.Contains(errMsg, "docker-compose.yml") {
		t.Errorf("refusal must name the modified file; got %q", errMsg)
	}
	if !strings.Contains(errMsg, "commit or revert") {
		t.Errorf("refusal must tell the operator to commit or revert; got %q", errMsg)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=dirty} delta = %v, want 1", delta)
	}
}

// (b) A clean clone whose fetch fails (bad remote URL) is refused with
// reason="fetch_error" and the message names the fetch error.
func TestPullDeployClone_FetchErrorRefused(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)
	pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n") // clone is now behind

	mustRun(t, clone, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "no-such-remote"))

	const repo = "test/fetch-refused"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "fetch_error"))

	errMsg := pullDeployClone(context.Background(), repo, clone, "main")
	if errMsg == "" {
		t.Fatal("failed fetch must refuse the deploy, got no error")
	}
	if !strings.Contains(errMsg, "fetch") {
		t.Errorf("refusal must name the fetch error; got %q", errMsg)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "fetch_error")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=fetch_error} delta = %v, want 1", delta)
	}
}

// (c) A clean clone behind origin fetches and fast-forwards — no refusal.
func TestPullDeployClone_BehindOriginFastForwards(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)
	newSHA := pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n")

	if errMsg := pullDeployClone(context.Background(), "test/ff", clone, "main"); errMsg != "" {
		t.Fatalf("clean clone behind origin must fast-forward, got refusal: %s", errMsg)
	}
	if head := gitOut(t, clone, "rev-parse", "HEAD"); head != newSHA {
		t.Errorf("clone HEAD = %s, want fast-forwarded to %s", head, newSHA)
	}
}

// (d) A local commit diverged from origin refuses the deploy with
// reason="diverged", naming both SHAs.
func TestPullDeployClone_DivergedRefused(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)
	remoteSHA := pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n")

	// Local commit on the deploy clone without pulling — diverged history.
	writeFile(t, filepath.Join(clone, "local-only.txt"), "local\n")
	mustRun(t, clone, "git", "add", ".")
	mustRun(t, clone, "git", "commit", "-m", "local commit")
	localSHA := gitOut(t, clone, "rev-parse", "HEAD")

	const repo = "test/diverged-refused"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "diverged"))

	errMsg := pullDeployClone(context.Background(), repo, clone, "main")
	if errMsg == "" {
		t.Fatal("diverged deploy clone must refuse the deploy, got no error")
	}
	if !strings.Contains(errMsg, ShortSHA(localSHA)) || !strings.Contains(errMsg, ShortSHA(remoteSHA)) {
		t.Errorf("refusal must name both SHAs (local %s vs origin %s); got %q",
			ShortSHA(localSHA), ShortSHA(remoteSHA), errMsg)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "diverged")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=diverged} delta = %v, want 1", delta)
	}
}

// (e) Untracked files only (agent-written plans/) never block the deploy.
func TestPullDeployClone_UntrackedProceeds(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)
	if err := os.MkdirAll(filepath.Join(clone, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(clone, "plans", "scratch.md"), "notes\n")

	if errMsg := pullDeployClone(context.Background(), "test/untracked", clone, "main"); errMsg != "" {
		t.Fatalf("untracked-only clone must proceed, got refusal: %s", errMsg)
	}
}

// (f) Dirty tree AND origin advanced — the exact #239 shape. The deploy is
// refused, and the fetch MUST have run before the dirty check: the clone's
// origin/main ref is advanced to the pushed SHA even though the pull was
// refused. A guard that checks dirty-first would leave origin/main stale.
func TestPullDeployClone_DirtyFetchRanFirst(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)
	newSHA := pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n")

	writeFile(t, filepath.Join(clone, "unrelated.txt"), "dirty\n")
	mustRun(t, clone, "git", "add", "unrelated.txt") // staged tracked change

	const repo = "test/dirty-fetch-first"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty"))

	errMsg := pullDeployClone(context.Background(), repo, clone, "main")
	if errMsg == "" {
		t.Fatal("dirty deploy clone must refuse the deploy, got no error")
	}
	if got := gitOut(t, clone, "rev-parse", "origin/main"); got != newSHA {
		t.Errorf("fetch must run BEFORE the dirty refusal: origin/main = %s, want %s", got, newSHA)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=dirty} delta = %v, want 1", delta)
	}
}

// TestPullDeployClone_PrefersCloneBranch — the deploy clone's own branch must
// win over the triggering repo's branch. Regression for the oxpulse-chat `dev`
// → krolik-server `main` mismatch that logged "git fetch failed: couldn't find
// remote ref dev" on every dev-branch deploy.
func TestPullDeployClone_PrefersCloneBranch(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	var fetched string
	withGitFetch(t, func(_ context.Context, _, branch string) error { fetched = branch; return nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "sha", nil })
	withGitPullFF(t, func(_ context.Context, _, _ string) error { return nil })

	if errMsg := pullDeployClone(context.Background(), "anatolykoptev/oxpulse-chat", "/fake/krolik-server", "dev"); errMsg != "" {
		t.Fatalf("unexpected refusal: %s", errMsg)
	}
	if fetched != "main" {
		t.Errorf("fetch should use the clone's branch (main), not the deploy branch (dev); got %q", fetched)
	}
}

// TestPullDeployClone_EmptyPath is a no-op (no clone configured, nothing verified).
func TestPullDeployClone_EmptyPath(t *testing.T) {
	// None of the runners should be called when clonePath is empty.
	called := false
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		called = true
		return nil, nil
	})

	if errMsg := pullDeployClone(context.Background(), "test/repo", "", "main"); errMsg != "" {
		t.Errorf("expected no refusal for empty path, got %q", errMsg)
	}
	if called {
		t.Error("gitStatusRunner must not be called when clonePath is empty")
	}
}

// TestPullDeployClone_UpToDate — remote has nothing new (HEAD == fetched ref).
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

	if errMsg := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main"); errMsg != "" {
		t.Errorf("expected no refusal for up-to-date clone, got %q", errMsg)
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

	if errMsg := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main"); errMsg != "" {
		t.Errorf("expected no refusal for fast-forward, got %q", errMsg)
	}
	if !pulled {
		t.Error("expected gitPullFFRunner to be called")
	}
}

// TestPullDeployClone_GitStatusError — git status itself fails: the clone's
// cleanliness is unverifiable, so the deploy is refused (fail-closed).
func TestPullDeployClone_GitStatusError(t *testing.T) {
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("not a git repository")
	})

	if errMsg := pullDeployClone(context.Background(), "test/repo", "/fake/clone", "main"); errMsg == "" {
		t.Error("unverifiable clone state must refuse the deploy, got no error")
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

	errMsg, _ := composeBuild(context.Background(), req, "/fake/worktree", "")
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
	errMsg, _ := composeBuild(context.Background(), req, "", "")
	if errMsg != "" {
		t.Fatalf("composeBuild no-worktree: unexpected error: %s", errMsg)
	}

	args := strings.Join(capturedArgs, " ")
	if !strings.Contains(args, "--build-arg OXPULSE_GIT_SHA=fa11bac") {
		t.Errorf("missing OXPULSE_GIT_SHA in no-worktree case; got: %s", args)
	}
}

// TestComposeBuild_ExtraBuildArgs_SHAPlaceholder verifies that per-repo
// BuildArgs are injected with ${SHA} substituted to the 12-char artifact tag
// derived from the FULL 40-char commit SHA via artifactTagSHA.
func TestComposeBuild_ExtraBuildArgs_SHAPlaceholder(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "samesha", nil })
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })

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
		CommitSHA: "9e57d2974426b7e070cb0deadbeefcafe1234567", // 40 chars → ${SHA} = "9e57d2974426"
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{"svc"},
			BuildArgs: []string{
				"WEB_ARTIFACT_IMAGE=oxpulse-chat-web:prod-${SHA}",
			},
		},
	}

	errMsg, _ := composeBuild(context.Background(), req, "/fake/worktree", "")
	if errMsg != "" {
		t.Fatalf("composeBuild: unexpected error: %s", errMsg)
	}

	args := strings.Join(capturedArgs, " ")
	// ${SHA} must be substituted with the 12-char artifact tag from the full SHA.
	want := "--build-arg WEB_ARTIFACT_IMAGE=oxpulse-chat-web:prod-9e57d2974426"
	if !strings.Contains(args, want) {
		t.Errorf("missing substituted build-arg; want %q in args: %s", want, args)
	}
	// The literal ${SHA} must NOT appear (unsubstituted placeholder = bug).
	if strings.Contains(args, "${SHA}") {
		t.Errorf("unsubstituted ${SHA} placeholder found in args: %s", args)
	}
}

// TestComposeBuild_DirtyCloneRefused — the deploy-clone guard sits in
// composeBuild itself so BOTH lanes (webhook executeBuild, manual
// executeManualComposeDeploy) refuse. A refused clone aborts the build
// before docker compose runs.
func TestComposeBuild_DirtyCloneRefused(t *testing.T) {
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(" M compose/search.yml\n"), nil
	})

	origBuild := buildRunner
	defer func() { buildRunner = origBuild }()
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		t.Error("docker compose build must NOT run when the deploy clone is refused")
		return nil, nil
	}

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "deadbeef",
		Config: RepoConfig{
			ComposePath:     "/fake/compose",
			SourcePath:      "/fake/source",
			DeployClonePath: "/fake/clone",
			Services:        []string{"svc"},
		},
	}

	errMsg, builtNow := composeBuild(context.Background(), req, "", "")
	if errMsg == "" {
		t.Fatal("composeBuild must fail when the deploy clone is dirty")
	}
	if !strings.Contains(errMsg, "compose/search.yml") {
		t.Errorf("error must name the modified file; got %q", errMsg)
	}
	if builtNow {
		t.Error("builtNow must be false on a refused deploy")
	}
}

// TestComposeBuild_FromDiskSkipsCloneVerify — a from_disk debug deploy builds
// the on-disk tree deliberately; the deploy-clone guard is skipped entirely
// (the server_deploy response says nothing was verified).
func TestComposeBuild_FromDiskSkipsCloneVerify(t *testing.T) {
	withGitFetch(t, func(_ context.Context, _, _ string) error {
		t.Error("gitFetchRunner must not run under from_disk — nothing is verified")
		return nil
	})
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		t.Error("gitStatusRunner must not run under from_disk — nothing is verified")
		return nil, nil
	})
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) { return "deadbee", nil })

	origBuild := buildRunner
	defer func() { buildRunner = origBuild }()
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "deadbeef",
		FromDisk:  true,
		Config: RepoConfig{
			ComposePath:     "/fake/compose",
			SourcePath:      "/fake/source",
			DeployClonePath: "/fake/clone",
			Services:        []string{"svc"},
		},
	}

	if errMsg, _ := composeBuild(context.Background(), req, "", ""); errMsg != "" {
		t.Fatalf("from_disk build must skip clone verification, got: %s", errMsg)
	}
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

// TestClassifyPorcelain covers the tracked/untracked split (used by
// pullDeployClone's dirty check and source_sync's collision detection).
func TestClassifyPorcelain(t *testing.T) {
	tracked, untracked := classifyPorcelain(" M compose.yml\n?? plans/a.md\nA  new.go\n?? x\n")
	if tracked != 2 || untracked != 2 {
		t.Errorf("classifyPorcelain = (%d, %d), want (2, 2)", tracked, untracked)
	}
}
