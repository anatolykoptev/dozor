package deploy

import (
	"context"
	"errors"
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
// RED-on-revert: replace gitPrepareBranch with composeBuild(ctx, req, "") —
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
