package deploy

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ── issues #173/#175-#179/#198: release diff on a stale clone ─────────────
//
// The whole class: releaseChangedFiles ran `git diff` against target_commitish
// in a source clone that had never fetched the release commit → exit 128 →
// conservative full build on every release, and build_paths gating never
// applied. The fix fetches the clone's origin BEFORE diffing.
//
// The fixture is a REAL three-repo topology, not a mock: an "origin" repo, a
// "work" clone that pushes the release commit, and a "source" clone that is
// stale (it only has the deployed commit). The oracle is git itself — if the
// fetch didn't run before the diff, `git diff` cannot resolve the target SHA
// and the test fails.

// buildStaleCloneFixture creates origin + work + stale source clones and
// returns (sourceDir, deployedSHA, targetSHA) where targetSHA exists on the
// remote but NOT yet in the stale source clone.
func buildStaleCloneFixture(t *testing.T, secondCommitPath, secondCommitContent string) (sourceDir, deployedSHA, targetSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	base := t.TempDir()

	// work repo — the "developer" clone that produces both commits.
	work := filepath.Join(base, "work")
	mustRun(t, base, "git", "init", "--initial-branch=main", work)
	mustRun(t, work, "git", "config", "user.email", "test@test.com")
	mustRun(t, work, "git", "config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(work, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "app", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "commit", "-m", "feat: app code")
	deployedSHA = mustRunOutput(t, work, "git", "rev-parse", "HEAD")

	// A file-path clone has its origin pointing at the work repo — dozor's
	// real clones point at github URLs, but `git fetch origin <branch>`
	// behaves identically on a path remote.
	sourceDir = filepath.Join(base, "source")
	mustRun(t, base, "git", "clone", work, sourceDir)
	mustRun(t, sourceDir, "git", "config", "user.email", "test@test.com")
	mustRun(t, sourceDir, "git", "config", "user.name", "Test")

	// The release commit lands on origin AFTER source was cloned — source is
	// now stale exactly like a production clone that hasn't fetched yet.
	target := filepath.Join(work, secondCommitPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(secondCommitContent), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "commit", "-m", "chore: release v1.0.0")
	targetSHA = mustRunOutput(t, work, "git", "rev-parse", "HEAD")

	// Prove the premise: the target SHA is NOT resolvable in the stale clone.
	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "--verify", "--quiet", targetSHA+"^{commit}") //nolint:gosec // test fixture
	cmd.Dir = sourceDir
	if err := cmd.Run(); err == nil {
		t.Fatalf("fixture is wrong: target SHA %s unexpectedly present in stale clone", targetSHA)
	}
	return sourceDir, deployedSHA, targetSHA
}

func TestReleaseChangedFiles_FetchesBeforeDiff(t *testing.T) {
	sourceDir, deployedSHA, targetSHA := buildStaleCloneFixture(t, "CHANGELOG.md", "# Changelog\n")
	rc := &RepoConfig{SourcePath: sourceDir, Branch: "main"}

	files, ok := releaseChangedFiles(context.Background(), rc, "anatolykoptev/x", targetSHA, func(string) string { return deployedSHA })
	if !ok {
		t.Fatal("releaseChangedFiles returned ok=false on a stale clone — the fetch did not run before the diff")
	}
	if len(files) != 1 || files[0] != "CHANGELOG.md" {
		t.Fatalf("files = %v, want [CHANGELOG.md]", files)
	}
	_ = deployedSHA // resolved internally from the clone's own HEAD
}

func TestReleaseChangedFiles_FetchFailureIsConservative(t *testing.T) {
	dir, _, targetSHA := buildTwoCommitFixture(t, "CHANGELOG.md", "# Changelog\n")
	rc := &RepoConfig{SourcePath: dir, Branch: "main"}

	orig := gitFetchRunner
	gitFetchRunner = func(context.Context, string, string) error { return errFetchBoom }
	t.Cleanup(func() { gitFetchRunner = orig })

	files, ok := releaseChangedFiles(context.Background(), rc, "anatolykoptev/x", targetSHA, func(string) string { return "unused" })
	if ok || files != nil {
		t.Fatalf("fetch failure must degrade to ok=false (conservative build), got ok=%v files=%v", ok, files)
	}
}

var errFetchBoom = errString("fetch boom")

type errString string

func (e errString) Error() string { return string(e) }

// ── issues #202/#203: pending-deploy key divergence ───────────────────────

func TestSetPendingDeploy_NormalizesBranchSuffixKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	ConfigurePendingDeployPersistence(path)
	t.Cleanup(func() { ConfigurePendingDeployPersistence("") })

	repo := "anatolykoptev/leak"
	svcs := []string{"svc-a"}

	// The manual path passes the config map key, which carries the target
	// suffix; the release path passes the bare webhook FullName. Both MUST
	// land on the same gauge label and the same persist key — otherwise the
	// series set at release time is never cleared at deploy time and
	// DozorReleaseWithheld can never observe a resolution.
	setPendingDeploy(repo+"#staging", svcs, 1)
	if got := testutil.ToFloat64(PendingDeployGauge.WithLabelValues(repo, "svc-a")); got != 1 {
		t.Fatalf("gauge[%q] = %v, want 1 — set under suffixed key did not normalise", repo, got)
	}
	setPendingDeploy(repo+"#staging", svcs, 0)
	if got := testutil.ToFloat64(PendingDeployGauge.WithLabelValues(repo, "svc-a")); got != 0 {
		t.Fatalf("gauge[%q] = %v after clear, want 0", repo, got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persist file: %v", err)
	}
	var doc pendingDeployFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("persist file corrupt: %v", err)
	}
	for k := range doc.Pending {
		t.Fatalf("persist key %q must be bare owner/repo, not a suffixed config key", k)
	}
}

// ── issue #165: stderr_tail keeps the END of captured output ─────────────

func TestTail_KeepsEnd(t *testing.T) {
	if got := tail("abcdef", 3); got != "...def" {
		t.Fatalf("tail head-drop: got %q, want %q", got, "...def")
	}
	if got := tail("ab", 5); got != "ab" {
		t.Fatalf("tail short: got %q", got)
	}
}

// Issue #165 call-site pin: runBuildWithFullLog must return the END of the
// captured output — docker errors sit after pages of progress spam, so a
// head-truncated message shows only the boilerplate and loses the cause.
func TestRunBuildWithFullLog_ReturnsTail(t *testing.T) {
	orig := buildRunner
	big := make([]byte, 0, maxOutputLen*3)
	big = append(big, []byte("HEADER-NOISE-")...)
	for i := 0; i < maxOutputLen*2; i++ {
		big = append(big, 'x')
	}
	big = append(big, []byte("REAL-CAUSE-AT-END")...)
	buildRunner = func(context.Context, string, []string) ([]byte, error) {
		return big, errFetchBoom
	}
	t.Cleanup(func() { buildRunner = orig })

	// The stderr tail reaches the operator via the ERROR log line, not the
	// return value (which points at the full dump file) — capture slog.
	var buf strings.Builder
	origLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(origLog) })

	_ = runBuildWithFullLog(context.Background(), BuildRequest{CommitSHA: "abc"}, nil)
	out := buf.String()
	if !strings.Contains(out, "REAL-CAUSE-AT-END") {
		t.Fatalf("stderr_tail lost the end (the actual cause): %q", out)
	}
	if strings.Contains(out, "HEADER-NOISE") {
		t.Fatalf("stderr_tail kept the head instead of the tail: %q", out)
	}
}

// ── issue #193: leaked ci-lock slot sweep ────────────────────────────────

func TestSweepDozorCiLockSlots_RemovesOnlyDozorOwned(t *testing.T) {
	runDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runDir)

	dozorKey := "dozor_oxbrowser-abc123-" + ciLockJob
	runnerKey := "gha_selfhosted-99-build"

	slot := filepath.Join(runDir, "ci-slot-2")
	if err := os.MkdirAll(slot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, "owner"), []byte(dozorKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "ci-job-"+dozorKey), []byte(slot), 0o600); err != nil {
		t.Fatal(err)
	}
	// A foreign CI-runner slot the sweep must not touch.
	foreign := filepath.Join(runDir, "ci-slot-1")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "owner"), []byte(runnerKey), 0o600); err != nil {
		t.Fatal(err)
	}

	SweepDozorCiLockSlots()

	if _, err := os.Stat(slot); !os.IsNotExist(err) {
		t.Fatalf("leaked dozor slot still present after sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "ci-job-"+dozorKey)); !os.IsNotExist(err) {
		t.Fatalf("leaked ci-job state file still present: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign runner slot removed by sweep: %v", err)
	}
}

// ── issue #174: startup reconcile re-drives missed deploys ───────────────

func TestReconcileMissedReleases_EnqueuesTagDrift(t *testing.T) {
	sourceDir := t.TempDir()
	foreignDeployClone := t.TempDir() // the compose repo — must NOT be consulted for the app SHA
	const tagObjSHA = "cccccccccccccccccccccccccccccccccccccccc"
	const tagSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // the peeled commit
	const deployedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	origURL, origLS, origSHA := gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup
	gitRemoteURLRunner = func(context.Context, string) (string, error) { return "fake-url", nil }
	// Real ls-remote order under --sort=-v:refname (verified on dozor's own
	// annotated tags): the peeled ^{} line sorts BEFORE its object line.
	// Include a second, older annotated tag to pin "newest semver only".
	// The target MUST be the peeled commit — a tag object SHA is not a
	// commit and poisons CommitSHA/DEPLOY_SHA.
	gitLsRemoteRunner = func(_ context.Context, _ string, pattern string, _ ...string) (string, error) {
		return tagSHA + "\trefs/tags/v1.2.3^{}\n" +
			tagObjSHA + "\trefs/tags/v1.2.3\n" +
			"dddddddddddddddddddddddddddddddddddddddd\trefs/tags/v1.2.2^{}\n" +
			"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee\trefs/tags/v1.2.2\n", nil
	}
	// The deployed receipt is the persisted last-built SHA keyed by bare repo —
	// answer correctly only for the canonical key to pin the wiring.
	deployedSHALookup = func(repo string) string {
		if repo == "anatolykoptev/x" {
			return deployedSHA
		}
		return ""
	}
	t.Cleanup(func() {
		gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup = origURL, origLS, origSHA
	})

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/x#staging": {
			SourcePath:      sourceDir,
			DeployClonePath: foreignDeployClone,
			Services:        []string{"svc"},
			DeployOn:        deployOnRelease,
		},
	}}
	q, _ := newTestQueue()
	ReconcileMissedReleases(context.Background(), cfg, q)

	if !q.queuedHas(serviceKey([]string{"svc"})) {
		t.Fatal("missed release drift was not enqueued")
	}
	req := q.pending[serviceKey([]string{"svc"})]
	if req.CommitSHA != tagSHA {
		t.Fatalf("enqueued SHA = %q, want peeled tag commit %q", req.CommitSHA, tagSHA)
	}
	if req.Repo != "anatolykoptev/x" {
		t.Fatalf("enqueued Repo = %q, want bare owner/repo (no #suffix)", req.Repo)
	}
}

// The deployed receipt is the persisted last-built SHA — a clone HEAD is NOT
// a receipt (deploy clone = compose repo; source clone ff is opportunistic).
// Pin: record persists under the bare key and lookup canonicalises suffixes.
func TestDeployedSHAReceipt_PersistAndLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployed.json")
	ConfigureDeployedSHAPersistence(path)
	t.Cleanup(func() { ConfigureDeployedSHAPersistence("") })

	recordDeployedSHA("anatolykoptev/x#staging", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if got := lookupDeployedSHA("anatolykoptev/x"); got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("lookup under bare key = %q", got)
	}
	if got := lookupDeployedSHA("anatolykoptev/x#prod"); got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("lookup under different suffix = %q — keys must canonicalise", got)
	}

	// Reload from disk — durability is the whole point.
	ConfigureDeployedSHAPersistence(path)
	if got := lookupDeployedSHA("anatolykoptev/x"); got == "" {
		t.Fatal("deployed SHA not restored from persist file")
	}
}

// sameSHA must treat a 7-char BuiltSHA and a 40-char ls-remote SHA as equal.
func TestSameSHA_PrefixTolerant(t *testing.T) {
	full := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if !sameSHA(full, "aaaaaaa") || !sameSHA("aaaaaaa", full) {
		t.Fatal("prefix comparison failed")
	}
	if sameSHA(full, "bbbbbbb") {
		t.Fatal("different SHAs must not compare equal")
	}
}

func TestReconcileMissedReleases_LightweightTag(t *testing.T) {
	dir := t.TempDir()
	const tagSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const deployedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	origURL, origLS, origSHA := gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup
	gitRemoteURLRunner = func(context.Context, string) (string, error) { return "fake-url", nil }
	// Lightweight tag: object line only, no ^{} follow-up.
	gitLsRemoteRunner = func(context.Context, string, string, ...string) (string, error) {
		return tagSHA + "\trefs/tags/v1.2.3\n", nil
	}
	deployedSHALookup = func(string) string { return deployedSHA }
	t.Cleanup(func() {
		gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup = origURL, origLS, origSHA
	})

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/x": {SourcePath: dir, Services: []string{"svc"}, DeployOn: deployOnRelease},
	}}
	q, _ := newTestQueue()
	ReconcileMissedReleases(context.Background(), cfg, q)
	if !q.queuedHas(serviceKey([]string{"svc"})) {
		t.Fatal("lightweight-tag drift was not enqueued")
	}
}

func TestReconcileMissedReleases_NoDriftNoSubmit(t *testing.T) {
	dir := t.TempDir()
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	origURL, origLS, origSHA := gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup
	gitRemoteURLRunner = func(context.Context, string) (string, error) { return "fake-url", nil }
	gitLsRemoteRunner = func(context.Context, string, string, ...string) (string, error) {
		return sha + "\trefs/tags/v1.2.3\n", nil
	}
	deployedSHALookup = func(string) string { return sha }
	t.Cleanup(func() {
		gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup = origURL, origLS, origSHA
	})

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/x": {SourcePath: dir, Services: []string{"svc"}, DeployOn: deployOnRelease},
	}}
	q, _ := newTestQueue()
	ReconcileMissedReleases(context.Background(), cfg, q)

	if q.queuedHas(serviceKey([]string{"svc"})) {
		t.Fatal("no drift but a build was submitted — reconcile must be quiet when in sync")
	}
}

func TestReconcileMissedReleases_UnresolvableSkips(t *testing.T) {
	dir := t.TempDir()

	origURL, origLS, origSHA := gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup
	gitRemoteURLRunner = func(context.Context, string) (string, error) { return "", errFetchBoom }
	gitLsRemoteRunner = func(context.Context, string, string, ...string) (string, error) { return "", errFetchBoom }
	deployedSHALookup = func(string) string { return "" }
	t.Cleanup(func() {
		gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup = origURL, origLS, origSHA
	})

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/x": {SourcePath: dir, Services: []string{"svc"}, DeployOn: deployOnRelease},
	}}
	q, _ := newTestQueue()
	ReconcileMissedReleases(context.Background(), cfg, q) // must not panic, must not submit

	if q.queuedHas(serviceKey([]string{"svc"})) {
		t.Fatal("unresolvable remote must not enqueue — reconcile never deploys on a guess")
	}
}
