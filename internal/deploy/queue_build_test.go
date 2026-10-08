package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// makeReq returns a minimal BuildRequest for executeBuild tests.
// SourcePath is empty to skip git steps; ComposePath is set to trigger docker steps.
func makeReq(composePath string) BuildRequest {
	return BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: composePath,
			Services:    []string{"svc"},
		},
	}
}

// composeSub returns the compose subcommand in an argv produced by
// composeArgv — {"compose", "-f", "<file>", "<sub>", ...} → "<sub>" — so
// test outputRunner stubs keep working now that -f shifts the subcommand
// off args[1]. Returns "" for argv that is not a compose call.
func composeSub(args []string) string {
	if len(args) < 4 || args[0] != "compose" || args[1] != "-f" {
		return ""
	}
	return args[3]
}

// zeroDelays sets healthWait, upRetryDelay, and portRecoveryWait to zero for fast tests,
// stubs buildRunner to a no-op, and stubs upRunner to a no-op so executeBuild tests
// don't shell out to real docker. Returns a restore function to be called via defer.
func zeroDelays(t *testing.T) func() {
	t.Helper()
	origHealth := healthWait
	origRetry := upRetryDelay
	origRecovery := portRecoveryWait
	origBuild := buildRunner
	origUp := upRunner
	healthWait = 0
	upRetryDelay = 0
	portRecoveryWait = 0
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	return func() {
		healthWait = origHealth
		upRetryDelay = origRetry
		portRecoveryWait = origRecovery
		buildRunner = origBuild
		upRunner = origUp
	}
}

func TestExecuteBuild_RetryThenSuccess(t *testing.T) {
	defer zeroDelays(t)()

	calls := 0
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("transient error output"), errors.New("exit status 1")
		}
		return nil, nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if calls != 2 {
		t.Fatalf("expected 2 docker up calls (retry), got %d", calls)
	}
	if strings.Contains(result.Error, "docker up") {
		t.Errorf("expected to pass docker up step, got error: %s", result.Error)
	}
}

func TestExecuteBuild_AllRetriesFail(t *testing.T) {
	defer zeroDelays(t)()

	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("permanent failure output"), errors.New("permanent failure")
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure")
	}
	want := "after 2 attempts"
	if !strings.Contains(result.Error, want) {
		t.Errorf("expected error to contain %q, got: %s", want, result.Error)
	}
}

func TestExecuteBuild_ContextCancelledDuringRetry(t *testing.T) {
	defer zeroDelays(t)()

	ctx, cancel := context.WithCancel(context.Background())

	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		cancel()
		return []byte("up failed output"), errors.New("up failed")
	}

	q := NewQueue(context.Background(), func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure on context cancellation")
	}
	want := "context cancelled during retry"
	if !strings.Contains(result.Error, want) {
		t.Errorf("expected error to contain %q, got: %s", want, result.Error)
	}
}

// TestExecuteBuild_EveryComposeArgvPassesF — EVERY `docker compose` dispatch
// in the deploy path must pin the project file explicitly with
// `-f <ComposePath>/docker-compose.yml`. Without -f, compose auto-loads an
// untracked docker-compose.override.yml / compose.override.yml dropped into
// the deploy clone, silently extending a verified deploy (issue #239).
//
// The test captures argv at all four dispatch seams (buildRunner, upRunner,
// outputRunner, cmdRunner) across THREE executeBuild runs — the happy path,
// the port-recovery force-recreate, and the rollback up — and asserts the -f
// pair on every compose invocation. Dropping -f from ANY call site (the up
// argv, the port-recovery recreate, or the rollback up included) turns this
// RED.
func TestExecuteBuild_EveryComposeArgvPassesF(t *testing.T) {
	defer zeroDelays(t)()

	const composePath = "/fake/compose"
	wantFile := filepath.Join(composePath, "docker-compose.yml")

	var composeCalls [][]string
	var recoveryArgv, rollbackArgv []string
	record := func(args []string) {
		if len(args) > 0 && args[0] == "compose" {
			argv := append([]string(nil), args...)
			composeCalls = append(composeCalls, argv)
			joined := strings.Join(argv, " ")
			// The port-recovery recreate and the rollback up both reach docker
			// via runCmd (not upRunner); tell them apart by --no-build.
			if strings.Contains(joined, "--force-recreate") {
				if strings.Contains(joined, "--no-build") {
					rollbackArgv = argv
				} else {
					recoveryArgv = argv
				}
			}
		}
	}

	buildRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		record(args)
		return nil, nil
	}
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			record(args)
		}
		return nil
	})

	// scenario picks the run variant: happy (healthy up), recovery (up ok,
	// port mapping lost → force-recreate, still unhealthy → rollback), and
	// rollback (up fails outright → rollback). The `images` payload differs
	// between the pre-up snapshot (prev image) and the rollback read (the
	// container's new image) so rollbackImages actually acts.
	type scenario int
	const (
		happy scenario = iota
		recovery
		failedUp
	)
	var active scenario
	upCalls := 0
	upRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		record(args)
		upCalls++
		if active == failedUp {
			return []byte("up exploded"), errors.New("up failed")
		}
		return nil, nil
	}
	imagesCalls := 0
	withOutputRunner(t, func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			record(args)
		}
		switch composeSub(args) {
		case "ps":
			if active == recovery {
				// Running but NO published ports — verifyPortMapping fails
				// against the config's declared ports → port recovery runs.
				return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
			}
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		case "config":
			// The service's image name + the port declaration verifyPortMapping checks.
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest","ports":["8080:8080"]}}}`), nil
		case "images":
			imagesCalls++
			if imagesCalls%2 == 1 {
				return []byte(`[{"ID":"previmg1234567","ContainerName":"svc"}]`), nil
			}
			// The container now runs a different image → rollback has work.
			return []byte(`[{"ID":"curimg0000000","ContainerName":"svc"}]`), nil
		}
		return nil, errors.New("unstubbed outputRunner call: " + strings.Join(args, " "))
	})

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	// Run 1: happy path.
	active = happy
	result := q.executeBuild(ctx, makeReq(composePath))
	if !result.Success {
		t.Fatalf("executeBuild must succeed on the happy path, got: %s", result.Error)
	}

	// Run 2: port recovery — the recreate runs, the re-check still reports
	// the lost port, and the failed deploy rolls back (so this run also
	// captures the rollback compose argv).
	active = recovery
	result = q.executeBuild(ctx, makeReq(composePath))
	if result.Success {
		t.Fatal("executeBuild must fail when port recovery does not restore the mapping")
	}

	// Run 3: the up itself fails → tryRollback → rollback compose up.
	active = failedUp
	result = q.executeBuild(ctx, makeReq(composePath))
	if result.Success {
		t.Fatal("executeBuild must fail when the up fails")
	}
	if !result.RolledBack {
		t.Error("a failed up with a previous image must roll back (RolledBack=true)")
	}

	if len(composeCalls) == 0 {
		t.Fatal("no docker compose calls captured — the test is not exercising the deploy path")
	}
	seen := map[string]bool{}
	for _, argv := range composeCalls {
		if len(argv) < 3 || argv[1] != "-f" || argv[2] != wantFile {
			t.Errorf("compose argv %v missing -f %s — override files in the clone would auto-load", argv, wantFile)
			continue
		}
		seen[composeSub(argv)] = true
	}
	for _, sub := range []string{"build", "up", "ps", "images", "config"} {
		if !seen[sub] {
			t.Errorf("expected a `compose %s` dispatch to be covered by the -f assertion", sub)
		}
	}
	// The two non-upRunner compose ups must both have been captured AND must
	// carry -f (the loop above already flagged a missing -f; these asserts
	// prove the paths were exercised at all).
	if recoveryArgv == nil {
		t.Error("port-recovery force-recreate never ran — the recovery up argv is untested for -f")
	}
	if rollbackArgv == nil {
		t.Error("rollback compose up never ran — the rollback argv is untested for -f")
	}
}

// ---------------------------------------------------------------------------
// Production-call-site coverage for the pre-up deploy-clone re-verify
// (issue #239 review, MAJOR 2): each test drives executeBuild /
// executeManualComposeDeploy with a REAL git deploy clone that is dirtied or
// moved between composeBuild's verification and the compose up. A mutant
// that removes the verification at any of these call sites turns the test
// RED — the refusal must reach docker never.
// ---------------------------------------------------------------------------

// deployCloneReq builds the executeBuild request over a real-git deploy
// clone: no SourcePath (gitPrepare no-ops), ComposePath = the clone (that is
// where the compose file lives on real deploys), CommitSHA deliberately not
// 40-hex so the no-new-image build-diff check stays a WARN.
func deployCloneReq(repo, clone string) BuildRequest {
	req := makeReq(clone)
	req.Repo = repo
	req.Config.DeployClonePath = clone
	return req
}

// (MAJOR 2a) executeBuild main-up call site: the clone is dirtied inside the
// `compose build` step — after pullDeployClone's build-time verification,
// before the up. The deploy must be refused WITHOUT docker up ever running
// and WITHOUT a rollback attempt (docker never ran — nothing to roll back;
// a rollback would re-verify the refused clone and double-count, MINOR 2).
func TestExecuteBuild_CloneDirtiedBetweenBuildAndUp_RefusedOnce(t *testing.T) {
	defer zeroDelays(t)()
	_, clone, _ := newDeployCloneFixture(t)

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		// The dirty lands AFTER composeBuild's clone verification.
		writeFile(t, filepath.Join(clone, "docker-compose.yml"), "version: '3'\n# dirtied mid-deploy\n")
		return nil, nil
	}
	upCalls := 0
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		upCalls++
		return nil, nil
	}
	var dockerCmds []string
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			dockerCmds = append(dockerCmds, strings.Join(args, " "))
		}
		return nil
	})
	imagesCalls := 0
	withOutputRunner(t, func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "images":
			imagesCalls++
			if imagesCalls == 1 { // pre-up snapshot: a previous image exists
				return []byte(`[{"ID":"previmg1111111","ContainerName":"svc"}]`), nil
			}
			// A rollback would see a DIFFERENT current image → it would act
			// (and re-refuse the dirty clone — that is what MINOR 2 forbids).
			return []byte(`[{"ID":"curimg2222222","ContainerName":"svc"}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest"}}}`), nil
		}
		return []byte("{}"), nil
	})

	const repo = "test/execbuild-dirty-between"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty"))

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	result := q.executeBuild(ctx, deployCloneReq(repo, clone))

	if result.Success {
		t.Fatal("executeBuild must fail when the clone is dirtied between build and up")
	}
	if !strings.Contains(result.Error, "docker-compose.yml") {
		t.Errorf("refusal must name the dirty file; got %q", result.Error)
	}
	if upCalls != 0 {
		t.Errorf("docker compose up ran %d times despite the refused clone", upCalls)
	}
	if strings.Contains(result.Error, "rollback") {
		t.Errorf("a pre-up refusal must not attempt a rollback (docker never ran); got %q", result.Error)
	}
	if len(dockerCmds) != 0 {
		t.Errorf("no docker command may run after a pre-up refusal; ran %v", dockerCmds)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty")) - before; delta != 1 {
		t.Errorf("refusal must be counted EXACTLY once — a rollback's re-verify would double-count; delta = %v", delta)
	}
}

// (MAJOR 2b) The port-recovery call site: the main up succeeds, the clone is
// dirtied inside it, then checkHealth reports a lost port mapping — the
// recovery's force-recreate must be refused before reaching docker.
func TestExecuteBuild_PortRecoveryCloneDirty_Refused(t *testing.T) {
	defer zeroDelays(t)()
	_, clone, _ := newDeployCloneFixture(t)

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		// Up succeeds; the dirty lands after its verify, before the recovery's.
		writeFile(t, filepath.Join(clone, "docker-compose.yml"), "version: '3'\n# dirtied before port recovery\n")
		return nil, nil
	}
	var dockerCmds []string
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			dockerCmds = append(dockerCmds, strings.Join(args, " "))
		}
		return nil
	})
	withOutputRunner(t, func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "ps":
			// Running but no publishers → port mapping lost → recovery path.
			return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest","ports":["8080:8080"]}}}`), nil
		case "images":
			return []byte(`[{"ID":"previmg1111111","ContainerName":"svc"}]`), nil
		}
		return []byte("{}"), nil
	})

	const repo = "test/execbuild-port-recovery-dirty"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty"))

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	result := q.executeBuild(ctx, deployCloneReq(repo, clone))

	if result.Success {
		t.Fatal("executeBuild must fail when the clone is dirty at port recovery")
	}
	if !strings.Contains(result.Error, "docker-compose.yml") {
		t.Errorf("refusal must name the dirty file; got %q", result.Error)
	}
	if len(dockerCmds) != 0 {
		t.Errorf("the recovery force-recreate must never reach docker on a refused clone; ran %v", dockerCmds)
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty")) - before; delta != 1 {
		t.Errorf("refusal counted delta = %v, want exactly 1 (the recovery refusal must not run a rollback re-verify)", delta)
	}
}

// (MAJOR 2c) The rollback call site: the up fails, the clone is dirtied in
// the LAST up attempt (after that attempt's verify), and the rollback's own
// re-verify must refuse — the current container is left running instead of
// recreating from unverified compose files.
func TestExecuteBuild_RollbackCloneDirty_Refused(t *testing.T) {
	defer zeroDelays(t)()
	_, clone, _ := newDeployCloneFixture(t)

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	upCalls := 0
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		upCalls++
		if upCalls == upMaxRetries {
			// The dirty lands after the last attempt's verify — the rollback
			// is the first code path that can see it.
			writeFile(t, filepath.Join(clone, "docker-compose.yml"), "version: '3'\n# dirtied before rollback\n")
		}
		return []byte("up exploded"), errors.New("up failed")
	}
	var dockerCmds []string
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			dockerCmds = append(dockerCmds, strings.Join(args, " "))
		}
		return nil
	})
	imagesCalls := 0
	withOutputRunner(t, func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "images":
			imagesCalls++
			if imagesCalls == 1 {
				return []byte(`[{"ID":"previmg1111111","ContainerName":"svc"}]`), nil
			}
			return []byte(`[{"ID":"curimg2222222","ContainerName":"svc"}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest"}}}`), nil
		}
		return []byte("{}"), nil
	})

	const repo = "test/execbuild-rollback-dirty"
	before := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty"))

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	result := q.executeBuild(ctx, deployCloneReq(repo, clone))

	if result.Success {
		t.Fatal("executeBuild must fail when the up fails")
	}
	if !strings.Contains(result.Error, "rollback also failed") {
		t.Errorf("a refused rollback must surface as 'rollback also failed'; got %q", result.Error)
	}
	if !strings.Contains(result.Error, "docker-compose.yml") {
		t.Errorf("rollback refusal must name the dirty file; got %q", result.Error)
	}
	for _, c := range dockerCmds {
		if strings.Contains(c, "tag ") || strings.Contains(c, "up ") {
			t.Errorf("rollback must not run %q on a refused clone — leave the current container", c)
		}
	}
	if delta := testutil.ToFloat64(DeployCloneRefusedTotal.WithLabelValues(repo, "dirty")) - before; delta != 1 {
		t.Errorf("the rollback refusal must be counted exactly once; delta = %v", delta)
	}
}

// (MAJOR 2c positive) A failed up on a CLEAN clone still rolls back —
// tryRollback must stay wired: the removal mutant `tryRollback → nil` turns
// this RED (RolledBack stays false, the tag/up never runs).
func TestExecuteBuild_UpFails_RollbackRuns(t *testing.T) {
	defer zeroDelays(t)()
	_, clone, _ := newDeployCloneFixture(t)

	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("up exploded"), errors.New("up failed")
	}
	var dockerCmds []string
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			dockerCmds = append(dockerCmds, strings.Join(args, " "))
		}
		return nil
	})
	imagesCalls := 0
	withOutputRunner(t, func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "images":
			imagesCalls++
			if imagesCalls == 1 {
				return []byte(`[{"ID":"previmg1111111","ContainerName":"svc"}]`), nil
			}
			return []byte(`[{"ID":"curimg2222222","ContainerName":"svc"}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest"}}}`), nil
		}
		return []byte("{}"), nil
	})

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	result := q.executeBuild(ctx, deployCloneReq("test/execbuild-rollback-runs", clone))

	if result.Success {
		t.Fatal("executeBuild must fail when the up fails")
	}
	if !result.RolledBack {
		t.Errorf("a failed up with a previous image must roll back; error: %q", result.Error)
	}
	var sawTag, sawRollbackUp bool
	for _, c := range dockerCmds {
		if strings.HasPrefix(c, "tag previmg1111111 ") {
			sawTag = true
		}
		if strings.Contains(c, "up -d") && strings.Contains(c, "--no-build") {
			sawRollbackUp = true
		}
	}
	if !sawTag {
		t.Errorf("rollback must retag the previous image; docker calls: %v", dockerCmds)
	}
	if !sawRollbackUp {
		t.Errorf("rollback must recreate via compose up --no-build; docker calls: %v", dockerCmds)
	}
}

// TestProcessBuild_CloneRefusalVisibility — a refused deploy must be LOUD
// and recoverable (issue #239): dozor_pending_deploy=1 for the repo so the
// dashboard shows a deploy is owed, the Telegram failure notify names the
// reason and the dirty paths, and NO deployed-SHA receipt is written — a
// refusal ships nothing.
func TestProcessBuild_CloneRefusalVisibility(t *testing.T) {
	defer zeroDelays(t)()

	// The clone verifies nothing: it is dirty at build time.
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withAttachedClone(t)
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(" M docker-compose.yml\n"), nil
	})

	const repo = "test/refusal-visible"
	const svc = "svc"
	PendingDeployGauge.WithLabelValues(repo, svc).Set(0)

	var notified []string
	ctx := context.Background()
	q := NewQueue(ctx, func(msg string) { notified = append(notified, msg) })
	defer q.Close()

	req := makeReq("/tmp")
	req.Repo = repo
	req.Config.DeployClonePath = "/fake/clone"
	q.processBuild(ctx, req, false)

	// 1. Pending gauge set — a refused deploy is a deploy still owed.
	if got := testutil.ToFloat64(PendingDeployGauge.WithLabelValues(repo, svc)); got != 1 {
		t.Errorf("dozor_pending_deploy{%s,%s} = %v, want 1 after a clone refusal", repo, svc, got)
	}

	// 2. The failure notify names the dirty path.
	var failMsg string
	for _, m := range notified {
		if strings.Contains(m, "FAILED") {
			failMsg = m
		}
	}
	if failMsg == "" {
		t.Fatalf("expected a FAILED Telegram notify on refusal, got: %v", notified)
	}
	if !strings.Contains(failMsg, "docker-compose.yml") || !strings.Contains(failMsg, "uncommitted changes") {
		t.Errorf("refusal notify must name the reason and dirty paths; got %q", failMsg)
	}

	// 3. No receipt — neither the source SHA nor the compose SHA may be
	// recorded: nothing shipped. RED-on-revert: write the receipt on the
	// refusal path and this fails.
	if got := lookupDeployedSHA(repo); got != "" {
		t.Errorf("a refused deploy must not record a deployed SHA, got receipt %q", got)
	}
	pendingDeployMu.Lock()
	_, composeReceipt := deployedComposeSHAs[repo]
	pendingDeployMu.Unlock()
	if composeReceipt {
		t.Error("a refused deploy must not record a compose SHA receipt")
	}
}

// TestProcessBuild_SuccessClearsRefusalAndRecordsComposeSHA — wiring coverage
// for two processBuild lines that previously had none (issue #239 review,
// MINOR 5):
//
//   - recordDeployedSHA(repo, sha, result.ComposeSHA) — the compose receipt
//     must carry the SHA the up actually ran against. RED when the
//     ComposeSHA arg is dropped.
//   - clearDeployRefusal(repo, services) — a successful deploy must clear a
//     refusal-set pending marker. RED when the call is removed.
func TestProcessBuild_SuccessClearsRefusalAndRecordsComposeSHA(t *testing.T) {
	defer zeroDelays(t)()

	// Stubbed git seams: clean clone at "clonesha0000", FETCH_HEAD == HEAD so
	// no merge runs; the pre-up verify sees HEAD == local origin ref and
	// accepts with upSHA = the clone's head.
	const cloneSHA = "c10a4b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b"
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withAttachedClone(t)
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return cloneSHA, nil })
	withGitMergeFF(t, func(_ context.Context, _, _ string) error { return nil })

	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "ps":
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest","ports":["8080:8080"]}}}`), nil
		case "images":
			return []byte(`[{"ID":"previmg1234567","ContainerName":"svc"}]`), nil
		}
		return []byte("{}"), nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })

	persist := filepath.Join(t.TempDir(), "deployed-sha.json")
	ConfigureDeployedSHAPersistence(persist)
	t.Cleanup(func() { ConfigureDeployedSHAPersistence("") })

	const repo = "test/processbuild-success"
	markDeployRefused(repo, []string{"svc"})
	t.Cleanup(func() {
		pendingDeployMu.Lock()
		delete(refusalPending, repo)
		pendingDeployMu.Unlock()
		PendingDeployGauge.WithLabelValues(repo, "svc").Set(0)
	})

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	req := makeReq("/fake/compose")
	req.Repo = repo
	req.Config.DeployClonePath = "/fake/clone"
	q.processBuild(ctx, req, false)

	// 1. The compose receipt records the clone SHA the up ran against —
	// dropping `result.ComposeSHA` from recordDeployedSHA leaves it empty.
	pendingDeployMu.Lock()
	gotCompose := deployedComposeSHAs[repo]
	pendingDeployMu.Unlock()
	if gotCompose != cloneSHA {
		t.Errorf("deployedComposeSHAs[%s] = %q, want %q — the receipt must carry the compose SHA", repo, gotCompose, cloneSHA)
	}

	// 2. The refusal marker is cleared and the gauge dropped — the owed
	// deploy shipped.
	pendingDeployMu.Lock()
	marked := refusalPending[repo]
	pendingDeployMu.Unlock()
	if marked {
		t.Error("a successful deploy must clear the refusal marker")
	}
	if got := testutil.ToFloat64(PendingDeployGauge.WithLabelValues(repo, "svc")); got != 0 {
		t.Errorf("dozor_pending_deploy = %v, want 0 after a successful deploy", got)
	}
}
