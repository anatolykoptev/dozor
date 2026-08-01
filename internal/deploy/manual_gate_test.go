package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// TestExecuteBuild_ManualGate_Binary_SetsPendingGauge — the binary/static gate
// returns early without building anything, but the user-visible state is the
// same as compose: a release happened and production did not get it. The gauge
// must therefore move 0→1 here too.
//
// This is not symmetry for its own sake. PreinitPendingDeployGauge seeds every
// manual repo at 0 regardless of kind, so a binary gate that skipped the signal
// would leave the series reading a confident 0 — "nothing is waiting" — while a
// release sat undeployed. An absent series reads as unknown; a stale 0 reads as
// healthy, which is worse and is exactly the quiet failure half 2 exists to
// prevent.
func TestExecuteBuild_ManualGate_Binary_SetsPendingGauge(t *testing.T) {
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error {
		t.Error("no build command must run for a binary manual repo")
		return nil
	})
	withSystemctlRunnerManual(t, func(_ context.Context, _ ...string) ([]byte, error) {
		t.Error("systemctl restart is the deploy — must not run")
		return []byte("active\n"), nil
	})
	const repo = "test/gauge-binary"
	const svc = "gauge-binary-svc"
	PendingDeployGauge.WithLabelValues(repo, svc).Set(0)
	if line := findPendingLine(t, repo, svc); !regexp.MustCompile(
		`^dozor_pending_deploy\{repo="test/gauge-binary",service="gauge-binary-svc"\} 0$`).MatchString(line) {
		t.Fatalf("pre-init line = %q, want anchored 0", line)
	}
	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	req := BuildRequest{
		Repo: repo,
		Config: RepoConfig{
			Kind:         KindBinary,
			SourcePath:   "/fake/source",
			BuildCmd:     []string{"go", "build", "-o", "/tmp/x", "./cmd/x"},
			UserServices: []string{svc},
			Services:     []string{svc},
			DeployOn:     "manual",
		},
	}
	result := q.executeBuild(ctx, req)
	if !result.Success || !result.ManualGated {
		t.Fatalf("expected manual-gated success, got %+v", result)
	}
	line := findPendingLine(t, repo, svc)
	if !regexp.MustCompile(`^dozor_pending_deploy\{repo="test/gauge-binary",service="gauge-binary-svc"\} 1$`).MatchString(line) {
		t.Fatalf("post-build line = %q, want anchored 1 (a withheld binary release must be visible)", line)
	}
}

// TestPendingDeployGauge_SurvivesRestart — THE load-bearing test for issue
// #188: a withheld release (gauge=1) must survive a dozor restart. Simulates
// a restart by: (1) setting the gauge to 1 via the real setPendingDeploy
// path (which persists), (2) resetting to 0 via PreinitPendingDeployGauge
// (as a fresh boot would), (3) calling RestorePendingDeployGauge, (4)
// asserting the gauge reads 1 again from the EXPORTED metric text.
//
// RED-on-revert: remove the persist call from setPendingDeploy, or remove
// RestorePendingDeployGauge — the gauge stays at 0 after the "restart" and
// the phase-3 assertion fails.
func TestPendingDeployGauge_SurvivesRestart(t *testing.T) {
	const repo = "test/survives-restart"
	const svc = "survive-svc"

	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-pending.json")
	ConfigurePendingDeployPersistence(path)
	defer ConfigurePendingDeployPersistence("")

	// Phase 1: a release is withheld → gate sets gauge to 1 (and persists).
	PendingDeployGauge.WithLabelValues(repo, svc).Set(0) // preinit
	setPendingDeploy(repo, []string{svc}, 1)
	if line := findPendingLine(t, repo, svc); !regexp.MustCompile(
		`^dozor_pending_deploy\{repo="test/survives-restart",service="survive-svc"\} 1$`).MatchString(line) {
		t.Fatalf("phase 1 line = %q, want anchored 1", line)
	}

	// Phase 2: simulate a restart — preinit resets to 0 (fresh boot).
	PreinitPendingDeployGauge(&Config{
		Repos: map[string]RepoConfig{
			repo + "#prod": {Services: []string{svc}, DeployOn: "manual"},
		},
	})
	if line := findPendingLine(t, repo, svc); !regexp.MustCompile(
		`^dozor_pending_deploy\{repo="test/survives-restart",service="survive-svc"\} 0$`).MatchString(line) {
		t.Fatalf("phase 2 (post-preinit) line = %q, want anchored 0", line)
	}

	// Phase 3: restore reads the persisted file and sets gauge back to 1.
	RestorePendingDeployGauge(&Config{
		Repos: map[string]RepoConfig{
			repo + "#prod": {Services: []string{svc}, DeployOn: "manual"},
		},
	})
	line := findPendingLine(t, repo, svc)
	if !regexp.MustCompile(`^dozor_pending_deploy\{repo="test/survives-restart",service="survive-svc"\} 1$`).MatchString(line) {
		t.Fatalf("phase 3 (post-restore) line = %q, want anchored 1 (withheld state must survive restart)", line)
	}
}

// TestPendingDeployGauge_DeployClearsPersistedState — stale-case test for
// direction 1 (persistence): when a manual deploy clears the gauge to 0
// (ExecuteManualDeploy → setPendingDeploy 0), the persisted state must also
// be cleared so a subsequent restart does not restore a stale 1. This is
// the mechanism that prevents staleness when the deploy goes through dozor.
func TestPendingDeployGauge_DeployClearsPersistedState(t *testing.T) {
	const repo = "test/stale-clear"
	const svc = "stale-svc"

	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-pending.json")
	ConfigurePendingDeployPersistence(path)
	defer ConfigurePendingDeployPersistence("")

	// A release was withheld → gauge=1, file has the entry.
	setPendingDeploy(repo, []string{svc}, 1)

	// A manual deploy clears the gauge → file entry removed.
	setPendingDeploy(repo, []string{svc}, 0)

	// Simulate a restart: preinit + restore.
	PreinitPendingDeployGauge(&Config{
		Repos: map[string]RepoConfig{
			repo + "#prod": {Services: []string{svc}, DeployOn: "manual"},
		},
	})
	RestorePendingDeployGauge(&Config{
		Repos: map[string]RepoConfig{
			repo + "#prod": {Services: []string{svc}, DeployOn: "manual"},
		},
	})
	line := findPendingLine(t, repo, svc)
	if !regexp.MustCompile(`^dozor_pending_deploy\{repo="test/stale-clear",service="stale-svc"\} 0$`).MatchString(line) {
		t.Fatalf("post-restore line = %q, want anchored 0 (deploy cleared persisted state)", line)
	}
}

// TestRestorePendingDeployGauge_DropsEntriesNoLongerManual — restore must
// reconcile the persisted state against the live config.
//
// The clearing path (ExecuteManualDeploy) is itself gated on the repo being
// deploy_on: manual, so an entry for a repo that was removed, flipped off
// manual, or whose service was renamed can never be cleared once restored. It
// would read 1 forever. A permanently stuck alarm is not a lesser failure than
// a stuck 0 — both end with operators ignoring the series.
func TestRestorePendingDeployGauge_DropsEntriesNoLongerManual(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-pending.json")
	ConfigurePendingDeployPersistence(path)
	t.Cleanup(func() { ConfigurePendingDeployPersistence("") })

	// State from before the restart: one repo still manual, one no longer.
	doc := pendingDeployFile{Pending: map[string][]string{
		"test/still-manual": {"live-svc"},
		"test/gone-manual":  {"stale-svc"},
	}}
	if err := writeJSONAtomic(path, doc); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	cfg := &Config{Repos: map[string]RepoConfig{
		"test/still-manual": {DeployOn: "manual", Services: []string{"live-svc"}},
		// test/gone-manual is deliberately absent — removed from config.
	}}

	RestorePendingDeployGauge(cfg)

	if line := findPendingLine(t, "test/still-manual", "live-svc"); !regexp.MustCompile(
		`^dozor_pending_deploy\{repo="test/still-manual",service="live-svc"\} 1$`).MatchString(line) {
		t.Fatalf("still-manual line = %q, want anchored 1", line)
	}
	if line := findPendingLine(t, "test/gone-manual", "stale-svc"); line != "" {
		t.Fatalf("gone-manual must NOT be restored (nothing could ever clear it), got %q", line)
	}

	// The file must be rewritten, or the next boot trips over the same entry.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "gone-manual") {
		t.Fatalf("stale entry still on disk after reconciliation: %s", raw)
	}
	if !strings.Contains(string(raw), "still-manual") {
		t.Fatalf("live entry lost during reconciliation: %s", raw)
	}
}

// TestReconcilePendingState — the pure reconciliation rule, tested directly
// rather than only through the restore path. It decides what survives a
// restart, and both directions of getting it wrong are silent: keep too much
// and a dead entry pins the gauge at 1 with no clearer left; keep too little
// and a genuinely withheld release disappears, which is the original bug.
func TestReconcilePendingState(t *testing.T) {
	allowed := map[string]map[string]bool{
		"o/live": {"a": true, "b": true},
	}
	doc := pendingDeployFile{Pending: map[string][]string{
		"o/live": {"a", "gone-svc"}, // one live service, one renamed away
		"o/dead": {"x"},             // repo no longer manual
	}}

	kept, dropped := reconcilePendingState(doc, allowed)

	if got := kept["o/live"]; len(got) != 1 || got[0] != "a" {
		t.Errorf("kept[o/live] = %v, want [a] (only the still-configured service)", got)
	}
	if _, ok := kept["o/dead"]; ok {
		t.Error("o/dead must not be kept: no clearer exists for a non-manual repo")
	}
	sort.Strings(dropped)
	want := []string{"o/dead svc=x", "o/live svc=gone-svc"}
	if !reflect.DeepEqual(dropped, want) {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
}

// TestReconcilePendingState_EmptyAllowedDropsAll documents the consequence of an
// empty allowed-set, which is why RestorePendingDeployGauge refuses to run with
// a nil config rather than calling this with nothing.
func TestReconcilePendingState_EmptyAllowedDropsAll(t *testing.T) {
	doc := pendingDeployFile{Pending: map[string][]string{"o/r": {"s"}}}
	kept, dropped := reconcilePendingState(doc, map[string]map[string]bool{})
	if len(kept) != 0 {
		t.Errorf("kept = %v, want empty", kept)
	}
	if len(dropped) != 1 {
		t.Errorf("dropped = %v, want exactly one entry", dropped)
	}
}

// TestRestorePendingDeployGauge_NilConfigLeavesStateIntact — a nil config must
// be a no-op, never a wipe. manualServiceSet(nil) is empty, so without the
// guard every entry would be dropped and the file rewritten empty, destroying
// exactly the state this feature exists to preserve.
func TestRestorePendingDeployGauge_NilConfigLeavesStateIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-pending.json")
	ConfigurePendingDeployPersistence(path)
	t.Cleanup(func() { ConfigurePendingDeployPersistence("") })

	if err := writeJSONAtomic(path, pendingDeployFile{
		Pending: map[string][]string{"test/nil-guard": {"svc"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	RestorePendingDeployGauge(nil)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), "nil-guard") {
		t.Fatalf("nil config wiped the persisted state: %s", raw)
	}
}
