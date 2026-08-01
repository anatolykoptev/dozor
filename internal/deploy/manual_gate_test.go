package deploy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// gatherDozorPendingDeployLines renders the exported text-exposition lines for
// the dozor_pending_deploy gauge from the default registry (the same registry
// /metrics exposes via promhttp.Handler). Used instead of an internal variable
// so a malformed/absent series is caught — a substring check on an internal
// value goes green on output the exporter never actually emits.
func gatherDozorPendingDeployLines(t *testing.T) []string {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("prometheus gather: %v", err)
	}
	var lines []string
	for _, mf := range mfs {
		if mf.GetName() != "dozor_pending_deploy" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetGauge() == nil {
				continue
			}
			var labels []string
			for _, lp := range m.GetLabel() {
				labels = append(labels, fmt.Sprintf(`%s=%q`, lp.GetName(), lp.GetValue()))
			}
			sort.Strings(labels) // prometheus sorts labels by name; sort again to be safe
			lines = append(lines, fmt.Sprintf("dozor_pending_deploy{%s} %g",
				strings.Join(labels, ","), m.GetGauge().GetValue()))
		}
	}
	return lines
}

// findPendingLine returns the gauge line for (repo, service) or "" if absent.
func findPendingLine(t *testing.T, repo, service string) string {
	t.Helper()
	want := fmt.Sprintf(`dozor_pending_deploy{repo=%q,service=%q}`, repo, service)
	for _, l := range gatherDozorPendingDeployLines(t) {
		if strings.HasPrefix(l, want) {
			return l
		}
	}
	return ""
}

// makeManualReq returns a BuildRequest for a deploy_on: manual compose repo.
// SourcePath is empty so gitPrepare is a no-op (matches makeReq in
// queue_build_test.go); ComposePath triggers the docker steps, stubbed via
// zeroDelays.
func makeManualReq(repo, composePath string, services ...string) BuildRequest {
	if len(services) == 0 {
		services = []string{"svc"}
	}
	return BuildRequest{
		Repo:      repo,
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: composePath,
			Services:    services,
			DeployOn:    "manual",
		},
	}
}

// F1 — TestExecuteBuild_ManualGate_HoldsDeploy: the manual gate must withhold
// the deploy (composeUp) for a release-triggered deploy_on: manual repo while
// still running the build. This is the falsification test demanded by the
// issue: with the compose manual gate REMOVED, a release-triggered deploy
// proceeds (composeUp runs) and this test goes RED.
//
// RED-on-revert: delete the `if req.Config.DeployOn == deployOnManual` block
// that returns before composeUp in executeBuild — upCalled becomes true and
// the assertion fails. (Verified by temporarily removing the gate.)
func TestExecuteBuild_ManualGate_HoldsDeploy(t *testing.T) {
	defer zeroDelays(t)()

	upCalled := false
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		upCalled = true
		return nil, nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeManualReq("test/f1-manual", "/tmp"))

	if !result.Success {
		t.Fatalf("expected success (artifact ready), got error: %s", result.Error)
	}
	if !result.ManualGated {
		t.Error("ManualGated must be true when deploy_on=manual withholds the deploy")
	}
	if upCalled {
		t.Error("composeUp must NOT run for deploy_on=manual (the gate withholds the deploy) — " +
			"if this fires, the manual gate in executeBuild was removed/bypassed")
	}
}

// TestExecuteBuild_ManualGate_Binary_SkipsBuild — for a binary manual repo the
// build IS the deploy (git pull + build + systemctl restart are inseparable),
// so the whole automatic build is skipped; server_deploy does it on demand.
// RED-on-revert: remove the binary/static early-return in the manual gate —
// executeBinaryBuild runs and cmdRunner fires, failing the assertion.
func TestExecuteBuild_ManualGate_Binary_SkipsBuild(t *testing.T) {
	cmdCalled := false
	withCmdRunner(t, func(_ context.Context, _ string, name string, _ ...string) error {
		if name == "git" || name == "go" {
			cmdCalled = true
		}
		return nil
	})
	withSystemctlRunnerManual(t, func(_ context.Context, _ ...string) ([]byte, error) {
		cmdCalled = true // systemctl restart is the deploy — must not run
		return []byte("active\n"), nil
	})

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	req := BuildRequest{
		Repo: "test/binary-manual",
		Config: RepoConfig{
			Kind:         KindBinary,
			SourcePath:   "/fake/source",
			BuildCmd:     []string{"go", "build", "-o", "/tmp/x", "./cmd/x"},
			UserServices: []string{"x"},
			Services:     []string{"x"},
			DeployOn:     "manual",
		},
	}
	result := q.executeBuild(ctx, req)

	if !result.Success {
		t.Fatalf("expected success (gate holds), got error: %s", result.Error)
	}
	if !result.ManualGated {
		t.Error("ManualGated must be true for a binary manual repo")
	}
	if cmdCalled {
		t.Error("no build/restart command must run for a binary manual repo on the automatic path")
	}
}

// TestExecuteBuild_ManualGate_SetsPendingGauge — when the manual gate holds,
// dozor_pending_deploy{repo,service} must read 1 (artifact ready, not
// deployed). Asserts the EXPORTED metric text, anchored, not an internal var.
func TestExecuteBuild_ManualGate_SetsPendingGauge(t *testing.T) {
	defer zeroDelays(t)()
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		t.Error("composeUp must not run")
		return nil, nil
	}

	const repo = "test/gauge-set"
	const svc = "gauge-svc"
	// Pre-initialise to 0 first so we prove the 0→1 transition.
	PendingDeployGauge.WithLabelValues(repo, svc).Set(0)
	if line := findPendingLine(t, repo, svc); !regexp.MustCompile(
		`^dozor_pending_deploy\{repo="test/gauge-set",service="gauge-svc"\} 0$`).MatchString(line) {
		t.Fatalf("pre-init line = %q, want anchored 0", line)
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	result := q.executeBuild(ctx, makeManualReq(repo, "/tmp", svc))
	if !result.Success || !result.ManualGated {
		t.Fatalf("expected manual-gated success, got %+v", result)
	}

	line := findPendingLine(t, repo, svc)
	if !regexp.MustCompile(`^dozor_pending_deploy\{repo="test/gauge-set",service="gauge-svc"\} 1$`).MatchString(line) {
		t.Fatalf("post-build line = %q, want anchored 1", line)
	}
}

// TestManualDeploy_ClearsPendingGauge — an explicit server_deploy
// (ExecuteManualDeploy) of a manual repo must clear dozor_pending_deploy back
// to 0 (the held artifact is now deployed). Asserts exported metric text.
func TestManualDeploy_ClearsPendingGauge(t *testing.T) {
	withManualFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withManualCurrentBranch(t, func(_ context.Context, _ string) (string, error) {
		return "main", nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })
	withOutputRunner(t, noopOutputRunner("clear-svc", "/fake/source"))
	defer zeroDelays(t)()

	const repo = "test/gauge-clear"
	const svc = "clear-svc"
	// Simulate the prior state: a release built the artifact and the gauge is 1.
	PendingDeployGauge.WithLabelValues(repo, svc).Set(1)

	req := ManualDeployRequest{
		Repo: repo,
		Config: RepoConfig{
			Branch:      "main",
			SourcePath:  "/fake/source",
			ComposePath: "/fake/compose",
			Services:    []string{svc},
			DeployOn:    "manual",
		},
	}
	result := ExecuteManualDeploy(context.Background(), req)
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}

	line := findPendingLine(t, repo, svc)
	if !regexp.MustCompile(`^dozor_pending_deploy\{repo="test/gauge-clear",service="clear-svc"\} 0$`).MatchString(line) {
		t.Fatalf("post-deploy line = %q, want anchored 0 (gauge cleared by server_deploy)", line)
	}
}

// TestPendingDeployGauge_PreinitialisedToZero — PreinitPendingDeployGauge must
// create the series at 0 for every service of every manual repo BEFORE any
// artifact is pending, so "nothing pending" (0) is distinguishable from "the
// exporter is not running" (series absent). Asserts exported metric text.
func TestPendingDeployGauge_PreinitialisedToZero(t *testing.T) {
	const repo = "test/gauge-preinit"
	cfg := &Config{
		Repos: map[string]RepoConfig{
			repo + "#prod": {
				Services: []string{"preinit-a", "preinit-b"},
				DeployOn: "manual",
			},
		},
	}

	PreinitPendingDeployGauge(cfg)

	for _, svc := range []string{"preinit-a", "preinit-b"} {
		line := findPendingLine(t, repo, svc)
		wantRe := fmt.Sprintf(`^dozor_pending_deploy\{repo="test/gauge-preinit",service=%q\} 0$`, svc)
		if !regexp.MustCompile(wantRe).MatchString(line) {
			t.Errorf("preinit line for %q = %q, want anchored 0", svc, line)
		}
	}
}

// TestExecuteBuild_ManualGate_NonManualRepo_Deploys — a deploy_on: release
// repo (NOT manual) must still deploy normally: composeUp runs and the gauge
// is NOT touched. Guards against the gate accidentally firing for non-manual
// repos. RED-on-revert: change the gate predicate to `!= ""` — composeUp is
// skipped for release repos too, upCalled stays false and this fails.
func TestExecuteBuild_ManualGate_NonManualRepo_Deploys(t *testing.T) {
	defer zeroDelays(t)()

	upCalled := false
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		upCalled = true
		return nil, nil
	}
	// checkHealth calls outputRunner for `docker compose ps --format json`.
	// Return a healthy, port-published container so the health check passes.
	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "compose" && args[1] == "ps" {
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		}
		return nil, nil
	})

	const repo = "test/release-deploys"
	const svc = "rel-svc"
	// Pre-set the gauge to 0 so we can assert it stays 0 (not set to 1).
	PendingDeployGauge.WithLabelValues(repo, svc).Set(0)

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	req := BuildRequest{
		Repo:      repo,
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: "/tmp",
			Services:    []string{svc},
			DeployOn:    "release",
		},
	}
	result := q.executeBuild(ctx, req)

	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
	if result.ManualGated {
		t.Error("ManualGated must NOT be true for a deploy_on: release repo")
	}
	if !upCalled {
		t.Error("composeUp MUST run for a deploy_on: release repo (only manual is gated)")
	}
	if line := findPendingLine(t, repo, svc); !regexp.MustCompile(
		`^dozor_pending_deploy\{repo="test/release-deploys",service="rel-svc"\} 0$`).MatchString(line) {
		t.Errorf("gauge for release repo = %q, want anchored 0 (untouched)", line)
	}
}
