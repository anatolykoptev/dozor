package deploy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// -- DI helpers for manual path seams (mirror queue_clone_pull_test.go style) --

func withManualFetch(t *testing.T, fn func(context.Context, string, string) error) {
	t.Helper()
	orig := gitManualFetchRunner
	gitManualFetchRunner = fn
	t.Cleanup(func() { gitManualFetchRunner = orig })
}

func withManualCurrentBranch(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := gitManualCurrentBranchRunner
	gitManualCurrentBranchRunner = fn
	t.Cleanup(func() { gitManualCurrentBranchRunner = orig })
}

func withCmdRunner(t *testing.T, fn func(context.Context, string, string, ...string) error) {
	t.Helper()
	orig := cmdRunner
	cmdRunner = fn
	t.Cleanup(func() { cmdRunner = orig })
}

func withShortSHARunnerManual(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := gitShortSHARunner
	gitShortSHARunner = fn
	t.Cleanup(func() { gitShortSHARunner = orig })
}

func withOriginSHARunner(t *testing.T, fn func(context.Context, string, string) (string, error)) {
	t.Helper()
	orig := gitManualOriginSHARunner
	gitManualOriginSHARunner = fn
	t.Cleanup(func() { gitManualOriginSHARunner = orig })
}

// withFullSHARunnerManual stubs gitFullSHARunner for the duration of the test.
func withFullSHARunnerManual(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := gitFullSHARunner
	gitFullSHARunner = fn
	t.Cleanup(func() { gitFullSHARunner = orig })
}

// withOutputRunner stubs the outputRunner used by resolveBuildOverrides inside
// composeBuild. The stub returns a minimal docker compose config JSON so that
// composeBuild can construct the build-context override without shelling out.
func withOutputRunner(t *testing.T, fn func(context.Context, string, string, ...string) ([]byte, error)) {
	t.Helper()
	orig := outputRunner
	outputRunner = fn
	t.Cleanup(func() { outputRunner = orig })
}

// noopOutputRunner returns a minimal docker compose config for a single service
// named svcName at sourcePath (same as the worktree root). Satisfies
// resolveBuildOverrides without real docker.
func noopOutputRunner(svcName, sourcePath string) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		// docker compose config --format json
		json := `{"services":{"` + svcName + `":{"build":{"context":"` + sourcePath + `"}}}}`
		return []byte(json), nil
	}
}

// collectCounterSum sums all label combinations for a CounterVec.
// Allows assertion without knowing exact labels up front.
func collectCounterSum(cv *prometheus.CounterVec) float64 {
	ch := make(chan prometheus.Metric, 64)
	cv.Collect(ch)
	close(ch)
	var sum float64
	for m := range ch {
		var metric dto.Metric
		if err := m.Write(&metric); err == nil && metric.Counter != nil {
			sum += metric.Counter.GetValue()
		}
	}
	return sum
}

// TestManualDeploy_DriftedClone_BuildsOriginMain — configured repo, source
// clone checked out on "dev" but configured branch is "main". The deploy must
// build a worktree at origin/main, not from on-disk HEAD.
//
// RED-on-revert: replace gitPrepareBranch with composeBuild(ctx, req, "", "") —
// worktreeTarget stays "" (no worktree add issued) and the assertion fails.
func TestManualDeploy_DriftedClone_BuildsOriginMain(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, branch string) error {
		if branch != "main" {
			t.Errorf("fetch must use configured branch 'main', got %q", branch)
		}
		return nil
	})
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "dev", nil // clone is on dev — drift scenario
	})

	var worktreeTarget string
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "git" && len(args) >= 4 && args[0] == "worktree" && args[1] == "add" {
			// git worktree add --detach <path> <target>
			worktreeTarget = args[len(args)-1]
		}
		return nil
	})
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return "abc1234", nil
	})
	// composeBuild calls resolveBuildOverrides → outputRunner for docker compose config.
	withOutputRunner(t, noopOutputRunner("oxpulse-chat", "/fake/source"))
	defer zeroDelays(t)()

	req := ManualDeployRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if worktreeTarget != "origin/main" {
		t.Errorf("expected worktree target 'origin/main', got %q", worktreeTarget)
	}
	if result.BuiltSHA == "" {
		t.Error("expected non-empty BuiltSHA")
	}
}

// TestManualDeploy_DriftedClone_EmitsMismatchCounter — drift fires
// ManualDeployBranchMismatchTotal.
//
// RED-on-revert: delete ManualDeployBranchMismatchTotal.WithLabelValues(...).Inc()
// from ExecuteManualDeploy — after stays equal to before and assertion fails.
func TestManualDeploy_DriftedClone_EmitsMismatchCounter(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "dev", nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	withOutputRunner(t, noopOutputRunner("oxpulse-chat", "/fake/source"))
	defer zeroDelays(t)()

	before := collectCounterSum(ManualDeployBranchMismatchTotal)

	req := ManualDeployRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
		},
	}
	_ = ExecuteManualDeploy(context.Background(), req)

	after := collectCounterSum(ManualDeployBranchMismatchTotal)
	if after <= before {
		t.Errorf("ManualDeployBranchMismatchTotal should increment on drift; before=%.0f after=%.0f", before, after)
	}
}

// TestManualDeploy_CloneOnMain_NoMismatch — no drift, mismatch counter must
// not fire.
//
// RED-on-revert: change `cloneBranch != branch` guard to `cloneBranch != ""`
// — counter fires even when clone is on main, causing this test to fail.
func TestManualDeploy_CloneOnMain_NoMismatch(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil // same as configured — no drift
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	withOutputRunner(t, noopOutputRunner("dozor", "/fake/source"))
	defer zeroDelays(t)()

	before := collectCounterSum(ManualDeployBranchMismatchTotal)

	req := ManualDeployRequest{
		Repo: "anatolykoptev/dozor",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"dozor"},
		},
	}
	_ = ExecuteManualDeploy(context.Background(), req)

	after := collectCounterSum(ManualDeployBranchMismatchTotal)
	if after != before {
		t.Errorf("mismatch counter must NOT fire when clone is on configured branch; before=%.0f after=%.0f", before, after)
	}
}

// TestManualDeploy_FromDisk_SkipsWorktree — from_disk=true must not issue
// "git worktree add" and must not call fetch.
//
// RED-on-revert: delete the req.FromDisk early-return path — "git worktree add"
// gets called and worktreeAdded becomes true, failing the assertion.
func TestManualDeploy_FromDisk_SkipsWorktree(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error {
		t.Error("git fetch must not be called in from_disk mode")
		return nil
	})
	worktreeAdded := false
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "git" && len(args) >= 2 && args[0] == "worktree" && args[1] == "add" {
			worktreeAdded = true
		}
		return nil
	})
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	defer zeroDelays(t)()

	req := ManualDeployRequest{
		Repo:     "anatolykoptev/oxpulse-chat",
		FromDisk: true,
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
		},
	}
	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("from_disk deploy should succeed: %s", result.Error)
	}
	if worktreeAdded {
		t.Error("from_disk=true must NOT issue 'git worktree add'")
	}
}

// TestManualDeploy_FetchFailure — git fetch error aborts the deploy.
func TestManualDeploy_FetchFailure(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error {
		return errors.New("network unreachable")
	})
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	defer zeroDelays(t)()

	req := ManualDeployRequest{
		Repo: "anatolykoptev/dozor",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"dozor"},
		},
	}
	result := ExecuteManualDeploy(context.Background(), req)

	if result.Success {
		t.Fatal("expected failure on fetch error")
	}
	if !strings.Contains(result.Error, "git fetch") {
		t.Errorf("expected error to mention 'git fetch', got: %s", result.Error)
	}
}

// withStaticScript stubs the staticScriptRunner for the duration of the test.
func withStaticScript(t *testing.T, fn func(context.Context, string, string, string, []string) ([]byte, error)) {
	t.Helper()
	orig := staticScriptRunner
	staticScriptRunner = fn
	t.Cleanup(func() { staticScriptRunner = orig })
}

// withSystemctlRunner stubs the systemctlRunner for the duration of the test.
func withSystemctlRunnerManual(t *testing.T, fn func(context.Context, ...string) ([]byte, error)) {
	t.Helper()
	orig := systemctlRunner
	systemctlRunner = fn
	t.Cleanup(func() { systemctlRunner = orig })
}

// TestManualDeploy_StaticKind_RunsScriptNotCompose — a KindStatic configured repo
// routed via ExecuteManualDeploy must run the static deploy script and must NOT
// call "docker compose build".
//
// RED-on-revert: remove the KindStatic case from ExecuteManualDeploy — the code
// falls through to executeManualComposeDeploy, which calls composeBuild and
// issues "docker compose", causing composeBuildCalled to become true and the
// assertion to fail. The staticScriptCalled assertion also fails because the
// script runner is never invoked.
func TestManualDeploy_StaticKind_RunsScriptNotCompose(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "def5678", nil })
	withOriginSHARunner(t, func(_ context.Context, _, _ string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil // full 40-char SHA
	})

	staticScriptCalled := false
	withStaticScript(t, func(_ context.Context, _, _, _ string, _ []string) ([]byte, error) {
		staticScriptCalled = true
		return []byte("static deploy OK"), nil
	})

	composeBuildCalled := false
	withOutputRunner(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		// outputRunner is only called by composeBuild / resolveBuildOverrides.
		composeBuildCalled = true
		return nil, nil
	})

	req := ManualDeployRequest{
		Repo: "anatolykoptev/dozor",
		Config: RepoConfig{
			Kind:               KindStatic,
			Branch:             "main",
			SourcePath:         "/fake/source",
			StaticDeployScript: "/home/krolik/bin/dozor-self-deploy.sh",
			Services:           []string{"anatolykoptev/dozor"},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if !staticScriptCalled {
		t.Error("static deploy script must be called for KindStatic repo")
	}
	if composeBuildCalled {
		t.Error("composeBuild must NOT be called for a KindStatic repo (would brick self-deploy)")
	}
}

// TestManualDeploy_BinaryKind_RunsBinaryNotCompose — a KindBinary configured repo
// routed via ExecuteManualDeploy must call executeBinaryBuild (git pull + build cmd +
// systemd restart) and must NOT call "docker compose build".
//
// RED-on-revert: remove the KindBinary case from ExecuteManualDeploy — the code
// falls through to executeManualComposeDeploy which issues "docker compose", causing
// composeBuildCalled to become true. The systemctl assertion also fails because
// executeBinaryBuild is never reached.
func TestManualDeploy_BinaryKind_RunsBinaryNotCompose(t *testing.T) {
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })

	// cmdRunner is used by runCmd — both git pull (executeBinaryBuild) and
	// docker compose (composeBuild) go through it. We track which commands fire.
	var gitPullCalled bool
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "git" && len(args) > 0 && args[0] == "pull" {
			gitPullCalled = true
		}
		return nil
	})

	systemctlCalled := false
	withSystemctlRunnerManual(t, func(_ context.Context, args ...string) ([]byte, error) {
		systemctlCalled = true
		return []byte("active\n"), nil
	})

	composeBuildCalled := false
	withOutputRunner(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		composeBuildCalled = true
		return nil, nil
	})

	req := ManualDeployRequest{
		Repo: "anatolykoptev/go-imagine",
		Config: RepoConfig{
			Kind:         KindBinary,
			Branch:       "main",
			SourcePath:   "/fake/source",
			BuildCmd:     []string{"go", "build", "-o", "/usr/local/bin/go-imagine", "./cmd/go-imagine"},
			UserServices: []string{"go-imagine"},
			Services:     []string{"go-imagine"},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if !gitPullCalled {
		t.Error("git pull must be called for KindBinary repo via executeBinaryBuild")
	}
	if !systemctlCalled {
		t.Error("systemctl restart must be called for KindBinary repo via executeBinaryBuild")
	}
	if composeBuildCalled {
		t.Error("composeBuild (docker compose) must NOT be called for a KindBinary repo")
	}
}

// TestManualDeploy_StaticKind_UsesOriginSHA — static path DEPLOY_SHA must come
// from origin/<branch> (gitManualOriginSHARunner), not from the on-disk HEAD.
//
// RED-on-revert: replace gitManualOriginSHARunner with resolveGitFullSHA(ctx, sourcePath) —
// the assertion `gotSHA != originSHA` fails because the static script is called with
// the HEAD value returned by gitFullSHARunner (headFullSHA) instead.
func TestManualDeploy_StaticKind_UsesOriginSHA(t *testing.T) {
	const originSHA = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"   // full 40-char hex
	const headFullSHA = "1234567890abcdef1234567890abcdef12345678" // full 40-char hex (different)
	const headShortSHA = "head456"                                 // short SHA for OXPULSE_GIT_SHA

	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	// Origin SHA runner returns a distinct full SHA — proves the static path reads origin.
	withOriginSHARunner(t, func(_ context.Context, _, branch string) (string, error) {
		if branch != "main" {
			t.Errorf("origin SHA runner: expected branch 'main', got %q", branch)
		}
		return originSHA, nil
	})
	// HEAD short SHA runner (for OXPULSE_GIT_SHA) would return a different value —
	// must NOT be the one used for DEPLOY_SHA.
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return headShortSHA, nil
	})
	// Full SHA runner (fallback) would also return a different value — must NOT be used.
	withFullSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return headFullSHA, nil
	})

	var gotSHA string
	withStaticScript(t, func(_ context.Context, _, _, commitSHA string, _ []string) ([]byte, error) {
		gotSHA = commitSHA
		return []byte("ok"), nil
	})

	req := ManualDeployRequest{
		Repo: "anatolykoptev/piter-now",
		Config: RepoConfig{
			Kind:               KindStatic,
			Branch:             "main",
			SourcePath:         "/fake/source",
			StaticDeployScript: "/home/krolik/bin/piter-deploy.sh",
			Services:           []string{"piter-now"},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if gotSHA != originSHA {
		t.Errorf("static script received SHA=%q; want origin SHA %q (not head SHA %q)",
			gotSHA, originSHA, headFullSHA)
	}
	if result.BuiltSHA != originSHA {
		t.Errorf("BuiltSHA=%q; want origin SHA %q", result.BuiltSHA, originSHA)
	}
}

// TestManualDeploy_BinaryKind_DriftEmitsMismatchCounter — binary path must emit
// ManualDeployBranchMismatchTotal when the source clone is on a different branch
// than the configured deploy branch.
//
// RED-on-revert: remove the drift-guard block from executeManualBinaryDeploy —
// after stays equal to before and the assertion fails.
func TestManualDeploy_BinaryKind_DriftEmitsMismatchCounter(t *testing.T) {
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "dev", nil // clone is on dev, configured is main — drift
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	withSystemctlRunnerManual(t, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("active\n"), nil
	})

	before := collectCounterSum(ManualDeployBranchMismatchTotal)

	req := ManualDeployRequest{
		Repo: "anatolykoptev/go-imagine",
		Config: RepoConfig{
			Kind:         KindBinary,
			Branch:       "main",
			SourcePath:   "/fake/source",
			BuildCmd:     []string{"go", "build", "-o", "/usr/local/bin/go-imagine", "./cmd/go-imagine"},
			UserServices: []string{"go-imagine"},
			Services:     []string{"go-imagine"},
		},
	}
	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("expected success: %s", result.Error)
	}
	after := collectCounterSum(ManualDeployBranchMismatchTotal)
	if after <= before {
		t.Errorf("ManualDeployBranchMismatchTotal must fire on binary drift; before=%.0f after=%.0f", before, after)
	}
}

// TestManualDeploy_BinaryKind_UsesHonestLabel — binary path must use trigger
// label "binary_pull", not "sha_pinned".
//
// RED-on-revert: change WithLabelValues("binary_pull",...) back to "sha_pinned" —
// the "binary_pull" counter stays at before, failing the assertion.
func TestManualDeploy_BinaryKind_UsesHonestLabel(t *testing.T) {
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil // no drift, isolate label test
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	withSystemctlRunnerManual(t, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("active\n"), nil
	})

	// Collect only the "binary_pull" slice before and after.
	binaryPullBefore := func() float64 {
		ch := make(chan prometheus.Metric, 64)
		ManualDeployTotal.Collect(ch)
		close(ch)
		var sum float64
		for m := range ch {
			var metric dto.Metric
			if err := m.Write(&metric); err != nil {
				continue
			}
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "trigger" && lp.GetValue() == "binary_pull" {
					if metric.Counter != nil {
						sum += metric.Counter.GetValue()
					}
				}
			}
		}
		return sum
	}

	before := binaryPullBefore()

	req := ManualDeployRequest{
		Repo: "anatolykoptev/go-imagine",
		Config: RepoConfig{
			Kind:         KindBinary,
			Branch:       "main",
			SourcePath:   "/fake/source",
			BuildCmd:     []string{"go", "build", "-o", "/usr/local/bin/go-imagine", "./cmd/go-imagine"},
			UserServices: []string{"go-imagine"},
			Services:     []string{"go-imagine"},
		},
	}
	_ = ExecuteManualDeploy(context.Background(), req)

	after := binaryPullBefore()
	if after <= before {
		t.Errorf("ManualDeployTotal{trigger=binary_pull} must fire for KindBinary; before=%.0f after=%.0f", before, after)
	}
}

// TestManualDeploy_ManualCounterFires_Success — ManualDeployTotal{result=success}
// must fire on a successful sha_pinned deploy.
func TestManualDeploy_ManualCounterFires_Success(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	withOutputRunner(t, noopOutputRunner("go-job", "/fake/source"))
	defer zeroDelays(t)()

	before := collectCounterSum(ManualDeployTotal)

	req := ManualDeployRequest{
		Repo: "anatolykoptev/go-job",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"go-job"},
		},
	}
	result := ExecuteManualDeploy(context.Background(), req)

	if !result.Success {
		t.Errorf("expected success: %s", result.Error)
	}
	after := collectCounterSum(ManualDeployTotal)
	if after <= before {
		t.Errorf("ManualDeployTotal should fire on success; before=%.0f after=%.0f", before, after)
	}
}

// TestManualDeploy_ComposeKind_ParticipatesInImageCache — a repo-configured
// KindCompose manual deploy with image_cache enabled must compute a tree hash
// from the worktree HEAD and attempt both the pull-before-build and the
// push-after-build, exactly like the webhook path.
//
// RED-on-revert: pass "" as treeHash to composeBuild in
// executeManualComposeDeploy — pull is skipped (sawPull stays false) and
// pushCachedImages is never called (sawPush stays false).
func TestManualDeploy_ComposeKind_ParticipatesInImageCache(t *testing.T) {
	const fakeTreeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	withGitTreeHashRunner(t, func(_ context.Context, _ string) (string, error) {
		return fakeTreeHash, nil
	})
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return "abc1234", nil
	})
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) { return "fake-token", nil })
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error { return nil })
	// composeImageName uses outputRunner; resolveBuildOverrides also uses it.
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))
	defer zeroDelays(t)()

	wantRef := cachedImageRef("ghcr.io/anatolykoptev/oxpulse-chat", fakeTreeHash)
	var sawPull, sawPush bool
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			sawPull = true
			// Cache miss — fall through to build.
			return errors.New("manifest unknown")
		}
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			sawPush = true
			if len(args) < 2 || args[1] != wantRef {
				t.Errorf("push ref: got %v, want %s", args, wantRef)
			}
		}
		return nil // tag, git worktree add, etc. succeed
	})

	req := ManualDeployRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if !sawPull {
		t.Error("manual deploy must attempt docker pull (pull-before-build) when image_cache is configured")
	}
	if !sawPush {
		t.Error("manual deploy must attempt docker push (push-after-build) when image_cache is configured")
	}
}

// TestManualDeploy_FromDisk_SkipsImageCacheWithReason — from_disk=true with
// image_cache configured must log an explicit, identifiable skip reason (not
// just "something was logged") and must NOT attempt pull or push.
//
// RED-on-revert: remove the from_disk skip log block — the reason string is
// absent from the captured logs and the assertion fails.
func TestManualDeploy_FromDisk_SkipsImageCacheWithReason(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return "abc1234", nil
	})
	defer zeroDelays(t)()

	var sawPull, sawPush bool
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			sawPull = true
		}
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			sawPush = true
		}
		return nil
	})

	req := ManualDeployRequest{
		Repo:     "anatolykoptev/oxpulse-chat",
		FromDisk: true,
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)
	if !result.Success {
		t.Errorf("from_disk deploy should succeed: %s", result.Error)
	}

	logOutput := buf.String()
	// Assert the specific reason — not just that something was logged.
	if !strings.Contains(logOutput, "image cache skipped") {
		t.Errorf("log must contain 'image cache skipped' reason; got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "from_disk builds the on-disk working tree") {
		t.Errorf("log must explain WHY caching is skipped (from_disk + uncommitted changes); got:\n%s", logOutput)
	}
	if sawPull {
		t.Error("from_disk must NOT attempt docker pull — caching is skipped")
	}
	if sawPush {
		t.Error("from_disk must NOT attempt docker push — caching is skipped")
	}
}

// TestManualDeploy_PushFailureDoesNotFailDeploy — a push failure on the manual
// path must NOT fail the deploy (best-effort), and must be logged at ERROR
// level so a silently-failing push is observable.
//
// RED-on-revert: make pushCachedImages return an error that the caller
// propagates — result.Success becomes false and the assertion fails.
func TestManualDeploy_PushFailureDoesNotFailDeploy(t *testing.T) {
	const fakeTreeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	withGitTreeHashRunner(t, func(_ context.Context, _ string) (string, error) {
		return fakeTreeHash, nil
	})
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return "abc1234", nil
	})
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) { return "fake-token", nil })
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error { return nil })
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))
	defer zeroDelays(t)()

	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			return errors.New("manifest unknown") // cache miss → build runs
		}
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			return errors.New("denied: permission_denied") // push fails
		}
		return nil // tag, git, etc. succeed
	})

	req := ManualDeployRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	result := ExecuteManualDeploy(context.Background(), req)
	if !result.Success {
		t.Errorf("push failure must NOT fail the deploy; got error: %s", result.Error)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "image cache push failed") {
		t.Errorf("push failure must be logged at ERROR with 'image cache push failed'; got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "permission_denied") {
		t.Errorf("ERROR log must include the underlying push error; got:\n%s", logOutput)
	}
}

// TestManualDeploy_ComposeLane_UsesFullSHA_AtTheCallSite guards the PRODUCTION
// call site, which TestCrossLane_TagParity does not.
//
// That test simulates the manual lane by calling resolveGitFullSHA and
// artifactTagSHA directly, so it proves the two helpers compose correctly and
// nothing about manual_deploy.go. Verified by mutation on 2026-08-06: reverting
// `CommitSHA: resolveGitFullSHA(...)` to `resolveGitSHA(...)` at
// manual_deploy.go:375 — the exact production defect this PR exists to fix —
// left BOTH TestCrossLane_TagParity and TestF1_ManualLaneShortSHAFails GREEN.
//
// A gate that survives the reintroduction of its own bug is not a gate. This
// test drives ExecuteManualDeploy end to end with both SHA runners mocked to
// return DIFFERENT values, and asserts the compose lane picked the full one.
//
// Mutation contract: manual_deploy.go:375
//
//	`CommitSHA: resolveGitFullSHA(ctx, worktreePath),`
//	-> `CommitSHA: resolveGitSHA(ctx, worktreePath),`
//
// must turn THIS test RED.
func TestManualDeploy_ComposeLane_UsesFullSHA_AtTheCallSite(t *testing.T) {
	const fullSHA = "9e57d2974426b7e070cb0deadbeefcafe1234567"
	const shortSHA = "9e57d2974" // what the old code produced — 9 chars, not 12

	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	// Both runners are mocked, and they DISAGREE. Whichever the call site reaches
	// for is the one that shows up in BuiltSHA, so the assertion cannot pass by
	// coincidence the way a same-value mock would let it.
	withFullSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return fullSHA, nil
	})
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) {
		return shortSHA, nil
	})
	withOutputRunner(t, noopOutputRunner("oxpulse-chat", "/fake/source"))
	defer zeroDelays(t)()

	result := ExecuteManualDeploy(context.Background(), ManualDeployRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
		},
	})

	if !result.Success {
		t.Fatalf("deploy failed: %s", result.Error)
	}
	if result.BuiltSHA != fullSHA {
		t.Fatalf("compose lane built with %q (len %d), want the FULL sha %q (len %d) — "+
			"a short sha here is the cross-lane tag-namespace bug: ${SHA:0:12} is a no-op "+
			"on it, so this lane tags artifacts the webhook lane can never find",
			result.BuiltSHA, len(result.BuiltSHA), fullSHA, len(fullSHA))
	}
	// The tag actually handed to WEB_ARTIFACT_IMAGE must be 12 chars.
	tag, err := artifactTagSHA(result.BuiltSHA)
	if err != nil {
		t.Fatalf("artifactTagSHA rejected the lane's own CommitSHA: %v", err)
	}
	if len(tag) != 12 {
		t.Fatalf("artifact tag %q has len %d, want 12", tag, len(tag))
	}
}
