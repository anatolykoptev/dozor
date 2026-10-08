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

// clonePullReq builds the BuildRequest pullDeployClone consumes in tests —
// the clone path/branch live on RepoConfig now, not in the call signature.
func clonePullReq(repo, clonePath string) BuildRequest {
	return BuildRequest{
		Repo: repo,
		Config: RepoConfig{
			DeployClonePath: clonePath,
			Services:        []string{"svc"},
		},
	}
}

// withAttachedClone stubs gitCurrentBranchRunner to report an attached HEAD
// on "main" — needed by every stubbed-runner pull test now that
// pullDeployClone refuses detached clones before the dirty check.
func withAttachedClone(t *testing.T) {
	t.Helper()
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
}

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

// withGitMergeFF stubs gitMergeFFRunner — the deploy-clone fast-forward seam
// (`git merge --ff-only <fetched sha>`). fn receives (ctx, dir, sha).
func withGitMergeFF(t *testing.T, fn func(context.Context, string, string) error) {
	t.Helper()
	orig := gitMergeFFRunner
	gitMergeFFRunner = fn
	t.Cleanup(func() { gitMergeFFRunner = orig })
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

	_, errMsg := pullDeployClone(context.Background(), clonePullReq(repo, clone))
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

	_, errMsg := pullDeployClone(context.Background(), clonePullReq(repo, clone))
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

	if _, errMsg := pullDeployClone(context.Background(), clonePullReq("test/ff", clone)); errMsg != "" {
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

	_, errMsg := pullDeployClone(context.Background(), clonePullReq(repo, clone))
	if errMsg == "" {
		t.Fatal("diverged deploy clone must refuse the deploy, got no error")
	}
	if !strings.Contains(errMsg, ShortSHA(localSHA)) || !strings.Contains(errMsg, ShortSHA(remoteSHA)) {
		t.Errorf("refusal must name both SHAs (local %s vs origin %s); got %q",
			ShortSHA(localSHA), ShortSHA(remoteSHA), errMsg)
	}
	// The refusal must carry the ff-only merge failure — "diverged" alone
	// sent operators on a wrong reconcile path when the real cause was an
	// untracked-file collision or rewritten history.
	if !strings.Contains(errMsg, "merge --ff-only") {
		t.Errorf("refusal must include the merge --ff-only error text; got %q", errMsg)
	}
	if !strings.Contains(errMsg, "fast-forward") && !strings.Contains(errMsg, "fatal") {
		t.Errorf("refusal must quote git's merge error; got %q", errMsg)
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

	if _, errMsg := pullDeployClone(context.Background(), clonePullReq("test/untracked", clone)); errMsg != "" {
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

	_, errMsg := pullDeployClone(context.Background(), clonePullReq(repo, clone))
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

// TestPullDeployClone_PrefersCloneBranch — the deploy clone's OWN configured
// branch (deploy_clone_branch, default main) must win over the triggering
// repo's Branch. Regression for the oxpulse-chat `dev` → krolik-server `main`
// mismatch that logged "git fetch failed: couldn't find remote ref dev" on
// every dev-branch deploy.
func TestPullDeployClone_PrefersCloneBranch(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withAttachedClone(t)
	var fetched string
	withGitFetch(t, func(_ context.Context, _, branch string) error { fetched = branch; return nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "sha", nil })
	withGitMergeFF(t, func(_ context.Context, _, _ string) error { return nil })

	req := clonePullReq("anatolykoptev/oxpulse-chat", "/fake/krolik-server")
	req.Config.Branch = "dev" // service repo branch — must NOT be used for the clone
	if _, errMsg := pullDeployClone(context.Background(), req); errMsg != "" {
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

	if _, errMsg := pullDeployClone(context.Background(), clonePullReq("test/repo", "")); errMsg != "" {
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
	withAttachedClone(t)
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		return sha, nil // FETCH_HEAD == HEAD
	})
	withGitMergeFF(t, func(_ context.Context, _, _ string) error {
		t.Error("gitMergeFFRunner must not be called when already up-to-date")
		return nil
	})

	if _, errMsg := pullDeployClone(context.Background(), clonePullReq("test/repo", "/fake/clone")); errMsg != "" {
		t.Errorf("expected no refusal for up-to-date clone, got %q", errMsg)
	}
}

// TestPullDeployClone_FastForward — remote has new commits; the clone
// fast-forwards with `git merge --ff-only <exact FETCH_HEAD sha>` — NEVER
// `git pull` (a pull re-fetches and can race origin forward past the
// verified ref, refusing as diverged on stale advice — #239 review).
func TestPullDeployClone_FastForward(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(""), nil
	})
	fetchCalls := 0
	withGitFetch(t, func(_ context.Context, _, _ string) error { fetchCalls++; return nil })
	withAttachedClone(t)
	calls := 0
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		calls++
		switch ref {
		case "FETCH_HEAD":
			return "newsha1234", nil
		default: // HEAD
			if calls <= 2 { //nolint:mnd // first two calls are FETCH_HEAD + HEAD before merge
				return "oldsha0000", nil
			}
			return "newsha1234", nil // HEAD after merge
		}
	})
	var mergedSHA string
	withGitMergeFF(t, func(_ context.Context, _, sha string) error {
		mergedSHA = sha
		return nil
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		t.Error("gitPullFFRunner must NOT run on the deploy-clone path — a pull re-fetches and races origin forward; merge the exact fetched SHA instead")
		return nil
	})

	if _, errMsg := pullDeployClone(context.Background(), clonePullReq("test/repo", "/fake/clone")); errMsg != "" {
		t.Errorf("expected no refusal for fast-forward, got %q", errMsg)
	}
	if mergedSHA != "newsha1234" {
		t.Errorf("merge --ff-only must target the exact fetched SHA %q, got %q", "newsha1234", mergedSHA)
	}
	if fetchCalls != 1 {
		t.Errorf("pullDeployClone must run exactly ONE fetch under the single fetch lock, got %d", fetchCalls)
	}
}

// TestPullDeployClone_GitStatusError — git status itself fails: the clone's
// cleanliness is unverifiable, so the deploy is refused (fail-closed).
func TestPullDeployClone_GitStatusError(t *testing.T) {
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withAttachedClone(t)
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("not a git repository")
	})

	if _, errMsg := pullDeployClone(context.Background(), clonePullReq("test/repo", "/fake/clone")); errMsg == "" {
		t.Error("unverifiable clone state must refuse the deploy, got no error")
	}
}

// TestPullDeployClone_DetachedRefused — a detached HEAD in the clone has no
// branch head to compare; refuse with reason="detached" (issue #239).
func TestPullDeployClone_DetachedRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)
	sha := gitOut(t, clone, "rev-parse", "HEAD")
	mustRun(t, clone, "git", "checkout", sha) // detach HEAD

	const repo = "test/detached-refused"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "detached"))

	_, errMsg := pullDeployClone(context.Background(), clonePullReq(repo, clone))
	if errMsg == "" {
		t.Fatal("detached deploy clone must refuse the deploy, got no error")
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "detached")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=detached} delta = %v, want 1", delta)
	}
}

// TestPullDeployClone_DeployCloneBranchConfig — deploy_clone_branch selects
// the clone's comparison branch even when it differs from the service branch.
// The clone must already sit on that branch (wrong_branch refusal, MINOR 4),
// so the current-branch seam reports "release".
func TestPullDeployClone_DeployCloneBranchConfig(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "release", nil })
	var fetched string
	withGitFetch(t, func(_ context.Context, _, branch string) error { fetched = branch; return nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "sha", nil })
	withGitMergeFF(t, func(_ context.Context, _, _ string) error { return nil })

	req := clonePullReq("test/repo", "/fake/clone")
	req.Config.Branch = "dev"
	req.Config.DeployCloneBranch = "release"
	if _, errMsg := pullDeployClone(context.Background(), req); errMsg != "" {
		t.Fatalf("unexpected refusal: %s", errMsg)
	}
	if fetched != "release" {
		t.Errorf("fetch should use deploy_clone_branch=release, got %q", fetched)
	}
}

// TestPullDeployClone_DefaultBranchMain verifies that deploy_clone_branch=""
// resolves to "main".
func TestPullDeployClone_DefaultBranchMain(t *testing.T) {
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withAttachedClone(t)
	var gotBranch string
	withGitFetch(t, func(_ context.Context, _, branch string) error {
		gotBranch = branch
		return nil
	})
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return "abc", nil })

	_, _ = pullDeployClone(context.Background(), clonePullReq("test/repo", "/fake/clone"))
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

	errMsg, _, _ := composeBuild(context.Background(), req, "/fake/worktree", "")
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
	errMsg, _, _ := composeBuild(context.Background(), req, "", "")
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

	errMsg, _, _ := composeBuild(context.Background(), req, "/fake/worktree", "")
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
	withAttachedClone(t)
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

	errMsg, builtNow, _ := composeBuild(context.Background(), req, "", "")
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

	if errMsg, _, _ := composeBuild(context.Background(), req, "", ""); errMsg != "" {
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

// ---------------------------------------------------------------------------
// Pre-up re-verification tests (issue #239, second stopgap layer).
//
// composeBuild's clone verification can be up to 45 minutes old when
// `docker compose up` actually runs — deploy-clone-sync or a human can
// dirty/move/detach the clone in between. verifyDeployCloneForUp re-checks
// the clone (local only, never re-fetching) right before EVERY up. These
// tests exercise the real-git path: pullDeployClone proves the clone good,
// the test then breaks it, and the up must refuse WITHOUT invoking docker.
// ---------------------------------------------------------------------------

// upCallCount installs an upRunner stub that counts invocations and returns
// its counter — a refused up must never reach docker.
func upCallCount(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	orig := upRunner
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		*calls++
		return nil, nil
	}
	t.Cleanup(func() { upRunner = orig })
	return calls
}

// (g) The clone is dirtied BETWEEN build and up: the build-time verification
// passed, then a tracked file was edited. composeUp must refuse with
// reason="dirty" and docker must never run — this is the ~45-minute window
// the build-time-only check left open.
func TestComposeUp_DirtyBetweenBuildAndUpRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)

	const repo = "test/up-dirty-refused"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	// The clone goes dirty after the build verified it.
	writeFile(t, filepath.Join(clone, "docker-compose.yml"), "version: '3'\n# sneaked in post-build\n")

	calls := upCallCount(t)
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty"))

	upErr, composeSHA, refused := composeUp(context.Background(), req, cv)
	if upErr == "" {
		t.Fatal("composeUp must refuse when the clone was dirtied between build and up")
	}
	if !refused {
		t.Error("a pre-up refusal must return refused=true so executeBuild skips rollback (docker never ran)")
	}
	if *calls != 0 {
		t.Errorf("docker compose up ran %d times despite a refused clone — the up must never reach docker", *calls)
	}
	if !strings.Contains(upErr, "docker-compose.yml") {
		t.Errorf("refusal must name the dirty file; got %q", upErr)
	}
	if composeSHA != "" {
		t.Errorf("a refused up must not report a compose SHA, got %q", composeSHA)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=dirty} delta = %v, want 1", delta)
	}
}

// (h) The clone's HEAD moves BETWEEN build and up (deploy-clone-sync pulled
// a newer origin, or a local commit landed). The up must refuse with
// reason="moved", naming both SHAs — the build verified one origin state and
// the up must never silently render another.
func TestComposeUp_MovedBetweenBuildAndUpRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)

	const repo = "test/up-moved-refused"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}
	atBuild := gitOut(t, clone, "rev-parse", "HEAD")

	// HEAD advances after the build-time verification (local commit keeps
	// the tree clean so ONLY the moved check can fire).
	writeFile(t, filepath.Join(clone, "advanced.txt"), "post-build\n")
	mustRun(t, clone, "git", "add", "advanced.txt")
	mustRun(t, clone, "git", "commit", "-m", "advanced after build")
	now := gitOut(t, clone, "rev-parse", "HEAD")

	calls := upCallCount(t)
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved"))

	upErr, _, _ := composeUp(context.Background(), req, cv)
	if upErr == "" {
		t.Fatal("composeUp must refuse when clone HEAD moved between build and up")
	}
	if *calls != 0 {
		t.Errorf("docker compose up ran %d times despite a moved clone", *calls)
	}
	if !strings.Contains(upErr, ShortSHA(atBuild)) || !strings.Contains(upErr, ShortSHA(now)) {
		t.Errorf("moved refusal must name both SHAs (at-build %s, now %s); got %q",
			ShortSHA(atBuild), ShortSHA(now), upErr)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=moved} delta = %v, want 1", delta)
	}
}

// (i) The clone is detached BETWEEN build and up. The up must refuse with
// reason="detached" — a detached HEAD has no branch line to compare.
func TestComposeUp_DetachedBetweenBuildAndUpRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)

	const repo = "test/up-detached-refused"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	sha := gitOut(t, clone, "rev-parse", "HEAD")
	mustRun(t, clone, "git", "checkout", sha) // detach HEAD post-build

	calls := upCallCount(t)
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "detached"))

	upErr, _, _ := composeUp(context.Background(), req, cv)
	if upErr == "" {
		t.Fatal("composeUp must refuse when the clone detached between build and up")
	}
	if *calls != 0 {
		t.Errorf("docker compose up ran %d times despite a detached clone", *calls)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "detached")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=detached} delta = %v, want 1", delta)
	}
}

// (j) A verified clone lets the up proceed and reports the clone's HEAD as
// the compose@<sha> the up ran against — the receipt/notify value.
func TestComposeUp_VerifiedCloneProceedsAndReportsSHA(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)

	const repo = "test/up-verified"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	calls := upCallCount(t)
	upErr, composeSHA, refused := composeUp(context.Background(), req, cv)
	if upErr != "" {
		t.Fatalf("verified clone must let the up proceed, got refusal: %s", upErr)
	}
	if refused {
		t.Error("a successful up must not be flagged refused")
	}
	if *calls != 1 {
		t.Errorf("expected exactly one docker compose up, got %d", *calls)
	}
	if want := gitOut(t, clone, "rev-parse", "HEAD"); composeSHA != want {
		t.Errorf("composeSHA = %q, want clone HEAD %q", composeSHA, want)
	}
}

// (k) The rollback up re-verifies too: a clone dirtied between build and a
// failed deploy's rollback must NOT drive a stale rollback — the current
// container is left running (no tag, no recreate).
func TestRollbackImages_CloneDirtiedBetweenBuildAndRollbackRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)

	const repo = "test/rollback-refused"
	req := clonePullReq(repo, clone)
	req.Config.ComposePath = clone
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	// The container's current image differs from the rollback target so the
	// rollback WOULD act — the clone check is what stops it.
	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if composeSub(args) == "images" {
			return []byte(`[{"ID":"curimg00000","ContainerName":"svc"}]`), nil
		}
		return []byte("{}"), nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, args ...string) error {
		t.Errorf("rollback must not run docker %v on a refused clone — leave the current container", args)
		return nil
	})

	// Dirty AFTER the build-time verification.
	writeFile(t, filepath.Join(clone, "docker-compose.yml"), "version: '3'\n# post-build dirt\n")

	err := rollbackImages(context.Background(), req, map[string]string{"svc": "previmg1234567"}, cv)
	if err == nil {
		t.Fatal("rollbackImages must refuse when the clone was dirtied after the build")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("rollback refusal must carry the clone refusal reason; got %q", err)
	}
}

// (l) allow_stale_config is the emergency escape: clone verification is
// skipped entirely (no fetch, no status), the deploy proceeds, and the
// override is LOUD — but an override is NOT a refusal, so it counts under
// dozor_deploy_clone_override_total{repo}, never refused_total{reason="override"}.
func TestComposeBuild_AllowStaleConfigSkipsVerify(t *testing.T) {
	withGitFetch(t, func(_ context.Context, _, _ string) error {
		t.Error("gitFetchRunner must NOT run under allow_stale_config — nothing is verified")
		return nil
	})
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		t.Error("gitStatusRunner must NOT run under allow_stale_config — nothing is verified")
		return nil, nil
	})
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) { return "deadbee", nil })

	origBuild := buildRunner
	defer func() { buildRunner = origBuild }()
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}

	const repo = "test/stale-override"
	before := testutil.ToFloat64(DeployCloneOverrideTotal.WithLabelValues(repo))
	beforeRefused := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "override"))

	req := BuildRequest{
		Repo:             repo,
		CommitSHA:        "deadbeef",
		AllowStaleConfig: true,
		Config: RepoConfig{
			ComposePath:     "/fake/compose",
			SourcePath:      "/fake/source",
			DeployClonePath: "/fake/clone",
			Services:        []string{"svc"},
		},
	}

	errMsg, _, cv := composeBuild(context.Background(), req, "", "")
	if errMsg != "" {
		t.Fatalf("allow_stale_config must let the build proceed, got: %s", errMsg)
	}
	if cv != nil {
		t.Error("allow_stale_config must produce no verification record — pre-up checks must skip too")
	}
	if delta := testutil.ToFloat64(DeployCloneOverrideTotal.WithLabelValues(repo)) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_override_total delta = %v, want 1", delta)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "override")) - beforeRefused; delta != 0 {
		t.Errorf("an override is not a refusal — refused_total{reason=override} delta = %v, want 0", delta)
	}
}

// (m) The server_deploy reply text must be truthful about what ran: the
// override path says "STALE CONFIG OVERRIDE" instead of pretending the
// compose was verified.
func TestComposeDeployDescription_StaleOverrideIsTruthful(t *testing.T) {
	withOriginSHARunner(t, func(_ context.Context, _ string, _ string) (string, error) {
		return "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeee", nil
	})

	desc := ComposeDeployDescription(context.Background(), ManualDeployRequest{
		Repo: "test/repo",
		Config: RepoConfig{
			SourcePath:      "/fake/source",
			DeployClonePath: "/fake/clone",
		},
		AllowStaleConfig: true,
	})
	if !strings.Contains(desc, "STALE CONFIG OVERRIDE") {
		t.Errorf("reply must say STALE CONFIG OVERRIDE under allow_stale_config; got %q", desc)
	}
	if strings.Contains(desc, "compose verified") {
		t.Errorf("reply must NOT claim verification under allow_stale_config; got %q", desc)
	}
}

// (n) The deployed-SHA receipt records the compose SHA the `up` ran against —
// compose@<sha> alongside the source SHA.
func TestRecordDeployedSHA_ComposeReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployed-sha.json")
	ConfigureDeployedSHAPersistence(path)
	t.Cleanup(func() { ConfigureDeployedSHAPersistence("") })

	const repo = "test/compose-receipt"
	const srcSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const cmpSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	recordDeployedSHA(repo, srcSHA, cmpSHA)

	pendingDeployMu.Lock()
	got := deployedComposeSHAs[repo]
	pendingDeployMu.Unlock()
	if got != cmpSHA {
		t.Errorf("deployedComposeSHAs[%s] = %q, want %q", repo, got, cmpSHA)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("receipt file must persist: %v", err)
	}
	if !strings.Contains(string(data), cmpSHA) {
		t.Errorf("persisted receipt must contain the compose SHA; got %s", data)
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

// ---------------------------------------------------------------------------
// Forward fast-forward acceptance (MAJOR 1, #239 review).
//
// Between build and up the deploy clone is LEGITIMATELY advanced to a newer
// origin/<branch> by a second queue worker, a manual server_deploy, or the
// deploy-clone-sync timer. The pre-up check accepts that: clean tree, attached
// HEAD on the configured branch, HEAD == local origin/<branch> ref, and the
// build-time commit is an ancestor of HEAD. Everything else is still refused.
// ---------------------------------------------------------------------------

// fastForwardClone simulates a second worker / deploy-clone-sync advancing the
// clone to the newest origin/<branch>: fetch + ff-only merge to the fetched
// tip. Returns the new HEAD.
func fastForwardClone(t *testing.T, clone string) string {
	t.Helper()
	mustRun(t, clone, "git", "fetch", "origin", "main", "--no-tags", "--quiet")
	fetched := gitOut(t, clone, "rev-parse", "FETCH_HEAD")
	mustRun(t, clone, "git", "merge", "--ff-only", fetched)
	return gitOut(t, clone, "rev-parse", "HEAD")
}

// (o) Forward fast-forward between build and up: a second worker pulled the
// clone to a NEWER origin/<branch> after our build verified it. The up must
// proceed — the compose files are still origin-verified, just newer — and the
// compose receipt records the CURRENT sha, not the build-time one.
func TestComposeUp_ForwardFFAccepted_ReportsNewSHA(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)

	const repo = "test/up-forward-ff"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}
	buildSHA := gitOut(t, clone, "rev-parse", "HEAD")

	// Origin advances, then the clone is fast-forwarded to it (as a second
	// queue worker or the deploy-clone-sync timer would).
	wantSHA := pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n")
	if got := fastForwardClone(t, clone); got != wantSHA {
		t.Fatalf("clone forward-ff = %s, want %s", got, wantSHA)
	}

	calls := upCallCount(t)
	upErr, composeSHA, refused := composeUp(context.Background(), req, cv)
	if upErr != "" {
		t.Fatalf("forward fast-forward must be ACCEPTED at up — got refusal: %s", upErr)
	}
	if refused {
		t.Error("a forward fast-forward is not a refusal")
	}
	if *calls != 1 {
		t.Errorf("expected exactly one docker compose up, got %d", *calls)
	}
	if composeSHA != wantSHA {
		t.Errorf("composeSHA = %q, want the up-time HEAD %q (not build-time %q)", composeSHA, wantSHA, buildSHA)
	}
}

// (p) A rewritten/rebased origin: the clone's HEAD still matches the local
// origin/<branch> ref, but the ref no longer descends from the commit the
// build verified. The compose files no longer derive from the verified line —
// refuse with reason="moved".
func TestComposeUp_NonDescendantBetweenBuildAndUpRefused(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)

	const repo = "test/up-non-descendant"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	// Rewrite origin history: amend the seed commit and force-push. The new
	// tip is NOT a descendant of the build-verified commit.
	mustRun(t, pusher, "git", "commit", "--amend", "-m", "rewritten history")
	mustRun(t, pusher, "git", "push", "--force", "origin", "main")
	// deploy-clone-sync/another worker would have moved the clone to it:
	// fetch (non-ff remote-tracking update) + hard reset to the new tip.
	mustRun(t, clone, "git", "fetch", "origin", "main", "--no-tags", "--quiet")
	mustRun(t, clone, "git", "reset", "--hard", "origin/main")
	// Tree is clean, attached, on main, HEAD == local origin/main — only the
	// ancestor check can refuse this.
	if out := gitOut(t, clone, "status", "--porcelain"); out != "" {
		t.Fatalf("clone must be clean for this test, got: %s", out)
	}

	calls := upCallCount(t)
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved"))

	upErr, _, refused := composeUp(context.Background(), req, cv)
	if upErr == "" {
		t.Fatal("composeUp must refuse when HEAD moved to a non-descendant of the build-verified commit")
	}
	if !refused {
		t.Error("a non-descendant move is a refusal (refused=true)")
	}
	if *calls != 0 {
		t.Errorf("docker compose up ran %d times on a non-descendant clone", *calls)
	}
	if !strings.Contains(upErr, "not a descendant") {
		t.Errorf("refusal must say the HEAD is not a descendant of the verified commit; got %q", upErr)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=moved} delta = %v, want 1", delta)
	}
}

// (q) The clone's checked-out branch differs from the configured
// deploy_clone_branch at up time (an operator switched it between build and
// up): refuse with reason="wrong_branch".
func TestComposeUp_WrongBranchBetweenBuildAndUpRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)

	const repo = "test/up-wrong-branch"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	mustRun(t, clone, "git", "checkout", "-b", "other-branch")

	calls := upCallCount(t)
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "wrong_branch"))

	upErr, _, refused := composeUp(context.Background(), req, cv)
	if upErr == "" {
		t.Fatal("composeUp must refuse when the clone is on the wrong branch")
	}
	if !refused {
		t.Error("a wrong-branch clone is a refusal (refused=true)")
	}
	if *calls != 0 {
		t.Errorf("docker compose up ran %d times on a wrong-branch clone", *calls)
	}
	if !strings.Contains(upErr, "other-branch") {
		t.Errorf("refusal must name the actual branch; got %q", upErr)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "wrong_branch")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=wrong_branch} delta = %v, want 1", delta)
	}
}

// (r) Rollback gets the same acceptance: the clone fast-forwarded to a newer
// origin between build and a failed deploy's rollback — the rollback must
// still run (it renders the newer-but-verified compose). Covers the rollback
// call site refusing on the old strict-equality rule.
func TestRollbackImages_ForwardFFAccepted(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)

	const repo = "test/rollback-forward-ff"
	req := clonePullReq(repo, clone)
	req.Config.ComposePath = clone
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	// The container's current image differs from the rollback target so the
	// rollback acts — the clone check is what could wrongly stop it.
	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "images":
			return []byte(`[{"ID":"curimg00000","ContainerName":"svc"}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest"}}}`), nil
		}
		return nil, errors.New("unstubbed outputRunner call: " + strings.Join(args, " "))
	})
	var dockerRan []string
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			dockerRan = append(dockerRan, strings.Join(args, " "))
		}
		return nil
	})

	// Forward-ff the clone AFTER the build-time verification.
	pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n")
	fastForwardClone(t, clone)

	err := rollbackImages(context.Background(), req, map[string]string{"svc": "previmg1234567"}, cv)
	if err != nil {
		t.Fatalf("rollback must proceed after a legitimate forward fast-forward, got: %v", err)
	}
	if len(dockerRan) == 0 {
		t.Error("rollback never ran docker — the forward-ff'd clone must not block it")
	}
}

// (s) The mirror refusal: origin history was rewritten between build and a
// failed deploy's rollback. The rollback must refuse — its compose file no
// longer derives from the verified line — and leave the current container
// running (no tag, no recreate).
func TestRollbackImages_NonDescendantRefused(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)

	const repo = "test/rollback-non-descendant"
	req := clonePullReq(repo, clone)
	req.Config.ComposePath = clone
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}

	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "images":
			return []byte(`[{"ID":"curimg00000","ContainerName":"svc"}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest"}}}`), nil
		}
		return nil, errors.New("unstubbed outputRunner call: " + strings.Join(args, " "))
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			t.Errorf("rollback must not run docker %v on a refused clone — leave the current container", args)
		}
		return nil
	})

	// Rewrite origin and move the clone to the rewritten tip.
	mustRun(t, pusher, "git", "commit", "--amend", "-m", "rewritten history")
	mustRun(t, pusher, "git", "push", "--force", "origin", "main")
	mustRun(t, clone, "git", "fetch", "origin", "main", "--no-tags", "--quiet")
	mustRun(t, clone, "git", "reset", "--hard", "origin/main")

	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved"))

	err := rollbackImages(context.Background(), req, map[string]string{"svc": "previmg1234567"}, cv)
	if err == nil {
		t.Fatal("rollbackImages must refuse when the clone moved to a non-descendant")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("rollback error must carry the clone refusal; got %q", err)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=moved} delta = %v, want 1", delta)
	}
}

// (t) The clone sits on a branch that is NOT the configured
// deploy_clone_branch at pull time: refuse with reason="wrong_branch". A
// branch switch is an operator action — never silently serve it.
func TestPullDeployClone_WrongBranchRefused(t *testing.T) {
	_, clone, _ := newDeployCloneFixture(t)
	mustRun(t, clone, "git", "checkout", "-b", "other-branch")

	const repo = "test/pull-wrong-branch"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "wrong_branch"))

	_, errMsg := pullDeployClone(context.Background(), clonePullReq(repo, clone))
	if errMsg == "" {
		t.Fatal("deploy clone on the wrong branch must refuse the deploy, got no error")
	}
	if !strings.Contains(errMsg, "other-branch") || !strings.Contains(errMsg, "main") {
		t.Errorf("refusal must name the actual and configured branches; got %q", errMsg)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "wrong_branch")) - before; delta != 1 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=wrong_branch} delta = %v, want 1", delta)
	}
}

// (u) Between build and up the clone's LOCAL refs/remotes/origin/<branch>
// ref advanced while HEAD stayed put (#248): deploy-clone-sync fetches
// without the deploy's fetch lock, and its merge step can no-op (an
// untracked-file collision, or the fetch/merge window). HEAD is still
// exactly what the build verified, so the up must proceed and report
// cv.headSHA — refusing it as "moved" was the bug.
func TestComposeUp_OriginRefAdvancedUnmovedHeadAccepted(t *testing.T) {
	_, clone, pusher := newDeployCloneFixture(t)

	const repo = "test/up-origin-ref-advanced"
	req := clonePullReq(repo, clone)
	cv, errMsg := pullDeployClone(context.Background(), req)
	if errMsg != "" {
		t.Fatalf("clean clone must verify at build time, got refusal: %s", errMsg)
	}
	buildSHA := gitOut(t, clone, "rev-parse", "HEAD")

	// Origin advances, then a bare fetch updates the clone's local
	// origin/main ref while HEAD stays — the shape an unlocked
	// deploy-clone-sync fetch leaves behind when nothing fast-forwards.
	newSHA := pushCommit(t, pusher, "docker-compose.yml", "version: '3'\nservices: {}\n")
	mustRun(t, clone, "git", "fetch", "origin", "main", "--no-tags", "--quiet")
	if got := gitOut(t, clone, "rev-parse", "origin/main"); got != newSHA {
		t.Fatalf("local origin/main must have advanced to %s, got %s", newSHA, got)
	}
	if got := gitOut(t, clone, "rev-parse", "HEAD"); got != buildSHA {
		t.Fatalf("HEAD must stay at the build-verified %s, got %s", buildSHA, got)
	}
	if out := gitOut(t, clone, "status", "--porcelain"); out != "" {
		t.Fatalf("clone must be clean for this test, got: %s", out)
	}

	calls := upCallCount(t)
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved"))

	upErr, composeSHA, refused := composeUp(context.Background(), req, cv)
	if upErr != "" {
		t.Fatalf("an unmoved HEAD must be accepted even when origin/<branch> advanced — got refusal: %s", upErr)
	}
	if refused {
		t.Error("an unmoved clone is not a refusal")
	}
	if *calls != 1 {
		t.Errorf("expected exactly one docker compose up, got %d", *calls)
	}
	if composeSHA != cv.headSHA {
		t.Errorf("composeSHA = %q, want the build-verified cv.headSHA %q", composeSHA, cv.headSHA)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "moved")) - before; delta != 0 {
		t.Errorf("dozor_deploy_clone_refused_total{reason=moved} delta = %v, want 0", delta)
	}
}
