package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeComposeFixture writes a deploy-clone-shaped compose project rooted at
// dir (composePath == deployClonePath, as in deploy-repos.yaml):
//
//	docker-compose.yml      include-only entry → compose/search.yml, compose/apps.yml
//	compose/search.yml      ox-browser (env_file ../config/services.env + map-form ../config/social.env), go-code
//	compose/apps.yml        go-job (env_file ../config/llm.env)
//	config/*.env            tracked env files
func writeComposeFixture(t *testing.T, dir string) {
	t.Helper()
	writeFixtureFile(t, dir, "docker-compose.yml", `name: fixtureproj
include:
  - compose/search.yml
  - compose/apps.yml
`)
	writeFixtureFile(t, dir, "compose/search.yml", `services:
  ox-browser:
    image: ox-browser:latest
    build:
      context: /fake/source
    env_file:
      - ../config/services.env
      - path: ../config/social.env
        required: false
    environment:
      - INTERNAL_SERVICE_SECRET=${INTERNAL_SERVICE_SECRET}
  go-code:
    image: go-code:latest
`)
	writeFixtureFile(t, dir, "compose/apps.yml", `services:
  go-job:
    image: go-job:latest
    env_file: ../config/llm.env
`)
	writeFixtureFile(t, dir, "config/services.env", "OX_BROWSER_URL=http://x\n")
	writeFixtureFile(t, dir, "config/social.env", "SOCIAL_API_TOKEN=x\n")
	writeFixtureFile(t, dir, "config/llm.env", "LLM_API_BASE=http://x\n")
}

func writeFixtureFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withComposeGuardGit stubs the git seams the stale-compose guard reads:
// the clone is on branch `main`; `diff` is the `git diff --name-only
// origin/main` output; `status` is the porcelain output. Any docker call
// (compose config / image inspect downstream of a guard pass) gets a
// minimal valid config model.
func withComposeGuardGit(t *testing.T, status, diff string) {
	t.Helper()
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(status), nil
	})
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		return "same-sha", nil // FETCH_HEAD == HEAD
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error { return nil })
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) { return "deadbee", nil })

	origOutput := outputRunner
	t.Cleanup(func() { outputRunner = origOutput })
	outputRunner = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte(diff), nil
		}
		return []byte(`{"name":"fixture","services":{"ox-browser":{"image":"ox-browser:latest","build":{"context":"/fake/source"}},"go-code":{"image":"go-code:latest"},"go-job":{"image":"go-job:latest"}}}`), nil
	}
}

// captureBuildRunner swaps buildRunner for a recorder; returns whether the
// docker compose build was invoked.
func captureBuildRunner(t *testing.T) *bool {
	t.Helper()
	called := new(bool)
	orig := buildRunner
	t.Cleanup(func() { buildRunner = orig })
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		*called = true
		return nil, nil
	}
	return called
}

func composeTestRequest(clone string, services ...string) BuildRequest {
	return BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "deadbee",
		Config: RepoConfig{
			ComposePath:     clone,
			DeployClonePath: clone,
			SourcePath:      "/fake/source",
			Services:        services,
		},
	}
}

// TestComposeBuild_StaleComposeInputBlocked is the issue-#239 regression test:
// a dirty deploy clone whose compose file for the deployed service diverges
// from origin/<branch> must NOT proceed to docker compose build/up — the
// recreated containers would carry stale config.
func TestComposeBuild_StaleComposeInputBlocked(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withComposeGuardGit(t, " M compose/search.yml\n", "compose/search.yml\n")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg == "" {
		t.Fatal("expected refusal to deploy from stale compose input, got success")
	}
	if !strings.Contains(errMsg, "compose/search.yml") {
		t.Errorf("error must name the stale compose input file, got: %s", errMsg)
	}
	if *built {
		t.Error("docker compose build must not run when compose input diverges from origin/<branch>")
	}
}

// TestComposeBuild_StaleComposeInputBehindOrigin: the dirty file itself is
// unrelated, but the clone's HEAD is BEHIND origin/<branch> — the compose file
// at the working tree differs from origin even without a local edit. Staleness
// is measured worktree-vs-origin, not dirtiness-vs-HEAD.
func TestComposeBuild_StaleComposeInputBehindOrigin(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withComposeGuardGit(t, " M plans/todo.md\n", "compose/search.yml\nplans/todo.md\n")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg == "" {
		t.Fatal("expected refusal when compose input differs from origin even without local compose edits")
	}
	if !strings.Contains(errMsg, "compose/search.yml") {
		t.Errorf("error must name compose/search.yml, got: %s", errMsg)
	}
	if *built {
		t.Error("build must not run")
	}
}

// TestComposeBuild_DirtyUnrelatedProceeds: a dirty clone whose divergence does
// not touch the deployed service's compose input proceeds with the existing
// WARN — sibling service files, plans, scratch are not ox-browser's input.
func TestComposeBuild_DirtyUnrelatedProceeds(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withComposeGuardGit(t, " M compose/apps.yml\n?? plans/foo.md\n", "compose/apps.yml\nplans/foo.md\n")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg != "" {
		t.Fatalf("unrelated dirty file must not block the deploy, got: %s", errMsg)
	}
	if !*built {
		t.Error("expected docker compose build to run")
	}
}

// TestComposeBuild_DirtyEnvFileBlocked: env_file content is baked into the
// container at create time, so a tracked env file consumed by the deployed
// service is compose input too.
func TestComposeBuild_DirtyEnvFileBlocked(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withComposeGuardGit(t, " M config/services.env\n", "config/services.env\n")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg == "" {
		t.Fatal("expected refusal when a consumed env_file diverges from origin")
	}
	if !strings.Contains(errMsg, "config/services.env") {
		t.Errorf("error must name config/services.env, got: %s", errMsg)
	}
	if *built {
		t.Error("build must not run")
	}
}

// TestComposeBuild_DirtyOtherServiceEnvFileProceeds: config/llm.env is only
// consumed by go-job — a dirty llm.env must not block an ox-browser deploy.
func TestComposeBuild_DirtyOtherServiceEnvFileProceeds(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withComposeGuardGit(t, " M config/llm.env\n", "config/llm.env\n")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg != "" {
		t.Fatalf("env file of an unrelated service must not block, got: %s", errMsg)
	}
	if !*built {
		t.Error("expected docker compose build to run")
	}
}

// TestComposeBuild_UntrackedOverrideBlocked: docker-compose.override.yml is
// auto-loaded by compose even when untracked — it never appears in git diff,
// so the porcelain untracked set must be intersected with compose input too.
// (.env itself is gitignored and deliberately NOT treated as input.)
func TestComposeBuild_UntrackedOverrideBlocked(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	writeFixtureFile(t, clone, "docker-compose.override.yml", `services:
  ox-browser:
    environment:
      - DEBUG=1
`)
	writeFixtureFile(t, clone, ".env", "INTERNAL_SERVICE_SECRET=x\n") // must not count as stale input
	withComposeGuardGit(t, "?? docker-compose.override.yml\n?? .env\n", "")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg == "" {
		t.Fatal("expected refusal on untracked auto-loaded override file")
	}
	if !strings.Contains(errMsg, "docker-compose.override.yml") {
		t.Errorf("error must name the override file, got: %s", errMsg)
	}
	if *built {
		t.Error("build must not run")
	}
}

// TestComposeBuild_CleanCloneUnchanged: a clean, converged clone deploys
// exactly as before — the guard runs but finds no divergence.
func TestComposeBuild_CleanCloneUnchanged(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	writeFixtureFile(t, clone, ".env", "INTERNAL_SERVICE_SECRET=x\n")
	withComposeGuardGit(t, "?? .env\n", "")
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg != "" {
		t.Fatalf("clean clone must proceed, got: %s", errMsg)
	}
	if !*built {
		t.Error("expected docker compose build to run")
	}
}

// TestComposeBuild_StaleCheckEvalError: when freshness cannot be evaluated
// (git diff fails), the deploy must fail closed — "cannot verify" is not
// "verified fresh".
func TestComposeBuild_StaleCheckEvalError(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withComposeGuardGit(t, " M compose/search.yml\n", "")
	orig := outputRunner
	outputRunner = func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return nil, errors.New("origin/main: unknown revision")
		}
		return nil, errors.New("docker stub unused")
	}
	t.Cleanup(func() { outputRunner = orig })
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg == "" {
		t.Fatal("expected fail-closed error when freshness cannot be evaluated")
	}
	if *built {
		t.Error("build must not run when the freshness check itself errors")
	}
}

// TestComposeBuild_NoDeployCloneUnchanged: repos without deploy_clone_path
// have no clone to verify against — the guard is a no-op.
func TestComposeBuild_NoDeployCloneUnchanged(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	built := captureBuildRunner(t)

	req := composeTestRequest(clone, "ox-browser")
	req.Config.DeployClonePath = ""
	errMsg, _ := composeBuild(context.Background(), req, "", "")
	if errMsg != "" {
		t.Fatalf("no deploy_clone_path → no guard, got: %s", errMsg)
	}
	if !*built {
		t.Error("expected docker compose build to run")
	}
}

// --- parser-level unit tests ---

// TestComposeInputSet_ForServices checks service→file attribution directly:
// the deployed service's input is its defining file + env_files + global files
// (entry + overrides); sibling files are excluded.
func TestComposeInputSet_ForServices(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)

	set, err := buildComposeInputSet(clone, clone)
	if err != nil {
		t.Fatalf("buildComposeInputSet: %v", err)
	}

	ox := set.forServices([]string{"ox-browser"})
	for _, want := range []string{"docker-compose.yml", "compose/search.yml", "config/services.env", "config/social.env"} {
		if _, ok := ox[want]; !ok {
			t.Errorf("ox-browser input missing %q (have %v)", want, ox)
		}
	}
	for _, not := range []string{"compose/apps.yml", "config/llm.env"} {
		if _, ok := ox[not]; ok {
			t.Errorf("ox-browser input must NOT contain %q", not)
		}
	}

	gj := set.forServices([]string{"go-job"})
	if _, ok := gj["config/llm.env"]; !ok {
		t.Error("go-job input must contain config/llm.env")
	}
	if _, ok := gj["config/services.env"]; ok {
		t.Error("go-job input must NOT contain config/services.env")
	}
}

// TestComposeInputSet_UnknownServiceConservative: a service absent from every
// parsed file cannot be attributed — conservatively the whole parsed input
// applies.
func TestComposeInputSet_UnknownServiceConservative(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)

	set, err := buildComposeInputSet(clone, clone)
	if err != nil {
		t.Fatalf("buildComposeInputSet: %v", err)
	}
	in := set.forServices([]string{"ghost-svc"})
	for _, want := range []string{"compose/search.yml", "compose/apps.yml", "config/llm.env"} {
		if _, ok := in[want]; !ok {
			t.Errorf("unknown-service input missing %q", want)
		}
	}
}

// TestComposeInputSet_GlobInclude: a glob include is expanded on disk; a
// changed path matching the pattern is potential input for every service.
func TestComposeInputSet_GlobInclude(t *testing.T) {
	clone := t.TempDir()
	writeFixtureFile(t, clone, "docker-compose.yml", "include:\n  - compose/*.yml\n")
	writeFixtureFile(t, clone, "compose/a.yml", "services:\n  svc-a:\n    image: a\n")
	writeFixtureFile(t, clone, "compose/b.yml", "services:\n  svc-b:\n    image: b\n")

	set, err := buildComposeInputSet(clone, clone)
	if err != nil {
		t.Fatalf("buildComposeInputSet: %v", err)
	}
	in := set.forServices([]string{"svc-a"})
	if _, ok := in["compose/a.yml"]; !ok {
		t.Errorf("svc-a input must contain compose/a.yml (have %v)", in)
	}
	// The wildcard pattern itself marks any other matching change as input.
	if !MatchPath("compose/b.yml", set.wilds) {
		t.Error("wildcard pattern compose/*.yml must match compose/b.yml")
	}
}

// TestComposeInputSet_MissingEntry: a composePath with no entry file cannot be
// verified — an error, never a silent pass.
func TestComposeInputSet_MissingEntry(t *testing.T) {
	clone := t.TempDir()
	if _, err := buildComposeInputSet(clone, clone); err == nil {
		t.Fatal("expected error for missing compose entry file")
	}
}

// TestComposeInputSet_ExtendsFile: a service extending a base in another file
// counts that file as its input.
func TestComposeInputSet_ExtendsFile(t *testing.T) {
	clone := t.TempDir()
	writeFixtureFile(t, clone, "docker-compose.yml", "include:\n  - compose/svc.yml\n")
	writeFixtureFile(t, clone, "compose/svc.yml", `services:
  svc:
    extends:
      file: base.yml
      service: base-svc
`)
	writeFixtureFile(t, clone, "compose/base.yml", "services:\n  base-svc:\n    image: base\n")

	set, err := buildComposeInputSet(clone, clone)
	if err != nil {
		t.Fatalf("buildComposeInputSet: %v", err)
	}
	in := set.forServices([]string{"svc"})
	if _, ok := in["compose/base.yml"]; !ok {
		t.Errorf("extends target compose/base.yml must be input for svc (have %v)", in)
	}
}

// TestStaleComposeInputs_UntrackedDotEnvIgnored pins the .env carve-out: the
// gitignored secrets file lives only in the live clone and can never diverge
// from origin — it must never block a deploy.
func TestStaleComposeInputs_UntrackedDotEnvIgnored(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	writeFixtureFile(t, clone, ".env", "INTERNAL_SERVICE_SECRET=x\n")
	withComposeGuardGit(t, "?? .env\n", "")

	stale, err := staleComposeInputs(context.Background(), clone, clone, "main", []string{"ox-browser"})
	if err != nil {
		t.Fatalf("staleComposeInputs: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("untracked .env must not be stale input, got %v", stale)
	}
}

// TestStaleComposeInputs_DivergedOutcomeGate documents that the guard is
// invoked whenever the pull did not converge — the stale check runs on
// diverged/error outcomes too, not just dirty_skipped. Exercised here via the
// fetch-error path: pull returns error, guard still evaluates and blocks.
func TestComposeBuild_StaleCheckAfterFetchError(t *testing.T) {
	clone := t.TempDir()
	writeComposeFixture(t, clone)
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error {
		return errors.New("could not resolve host: github.com")
	})
	withGitShortSHA(t, func(_ context.Context, _ string) (string, error) { return "deadbee", nil })
	orig := outputRunner
	t.Cleanup(func() { outputRunner = orig })
	outputRunner = func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte("compose/search.yml\n"), nil // last-known origin ref still differs
		}
		return []byte(`{"services":{}}`), nil
	}
	built := captureBuildRunner(t)

	errMsg, _ := composeBuild(context.Background(), composeTestRequest(clone, "ox-browser"), "", "")
	if errMsg == "" {
		t.Fatal("expected refusal: compose input diverges from last-known origin ref after failed fetch")
	}
	if *built {
		t.Error("build must not run")
	}
}
