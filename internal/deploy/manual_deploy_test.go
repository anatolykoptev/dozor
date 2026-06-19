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
