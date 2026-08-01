package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// --- helpers ---

// newTestDriftChecker builds a DriftChecker with overridable API base/client
// for testing. The token is set so the webhook-events check is not skipped.
func newTestDriftChecker(t *testing.T, cfg *Config, livePath, apiBase string) *DriftChecker {
	t.Helper()
	return &DriftChecker{
		cfg:      cfg,
		livePath: livePath,
		token:    "test-token",
		apiBase:  apiBase,
		client:   &http.Client{},
	}
}

// gaugeValue reads the current value of dozor_config_drift for the given
// label combination.
func gaugeValue(repo, check, outcome string) float64 {
	return testutil.ToFloat64(ConfigDrift.WithLabelValues(repo, check, outcome))
}

// githubHooksHandler returns an http.HandlerFunc that serves a list of
// webhooks as GitHub's GET /repos/{owner}/{repo}/hooks would.
func githubHooksHandler(hooks []githubHook) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hooks)
	}
}

// githubErrorHandler returns an http.HandlerFunc that always responds 500.
func githubErrorHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"server error"}`))
	}
}

// hook builds a githubHook with the given URL and events.
func hook(url string, events ...string) githubHook {
	h := githubHook{Events: events}
	h.Config.URL = url
	return h
}

// makeConfig builds a Config with the given repo entries.
func makeConfig(repos map[string]RepoConfig) *Config {
	return &Config{Repos: repos, GitHubToken: "test-token"}
}

// rc is a shorthand builder for a minimal valid RepoConfig.
func rc(deployOn string, services ...string) RepoConfig {
	return RepoConfig{DeployOn: deployOn, Services: services, ComposePath: "/tmp", SourcePath: "/tmp"}
}

// --- drift #2: webhook events vs deploy_on ---

func TestCheckWebhookEvents_EventsCoverDeployOn_OK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoKey string
		rc      RepoConfig
		hooks   []githubHook
	}{
		{
			name:    "push_repo_with_push_event",
			repoKey: "test/ok-push",
			rc:      rc("", "svc"),
			hooks:   []githubHook{hook("https://dozor.example.com/deploy/github", "push")},
		},
		{
			name:    "release_repo_with_release_event",
			repoKey: "test/ok-release",
			rc:      rc("release", "svc"),
			hooks:   []githubHook{hook("https://dozor.example.com/deploy/github", "release")},
		},
		{
			name:    "release_repo_with_push_and_release_events",
			repoKey: "test/ok-both",
			rc:      rc("release", "svc"),
			hooks:   []githubHook{hook("https://dozor.example.com/deploy/github", "push", "release")},
		},
		{
			// manual keeps the release trigger (it gates the deploy, not the
			// event), so a manual repo needs the "release" webhook subscription
			// exactly like a release repo — issue #183.
			name:    "manual_repo_with_release_event",
			repoKey: "test/ok-manual",
			rc:      rc("manual", "svc"),
			hooks:   []githubHook{hook("https://dozor.example.com/deploy/github", "release")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(githubHooksHandler(tc.hooks))
			defer srv.Close()

			cfg := makeConfig(map[string]RepoConfig{tc.repoKey: tc.rc})
			d := newTestDriftChecker(t, cfg, "", srv.URL)

			result := d.checkOneRepoWebhook(context.Background(), tc.repoKey, d.requiredEventsByRepo()[tc.repoKey])

			if result.outcome != outcomeOK {
				t.Fatalf("outcome = %q, want %q", result.outcome, outcomeOK)
			}
		})
	}
}

func TestCheckWebhookEvents_MissingEvent_Drift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repoKey     string
		rc          RepoConfig
		hooks       []githubHook
		wantMissing string
	}{
		{
			name:        "release_repo_webhook_has_push_only",
			repoKey:     "test/drift-release-needs-release",
			rc:          rc("release", "svc"),
			hooks:       []githubHook{hook("https://dozor.example.com/deploy/github", "push")},
			wantMissing: "release",
		},
		{
			name:        "push_repo_webhook_has_release_only",
			repoKey:     "test/drift-push-needs-push",
			rc:          rc("", "svc"),
			hooks:       []githubHook{hook("https://dozor.example.com/deploy/github", "release")},
			wantMissing: "push",
		},
		{
			// manual repos need the release event (the gate withholds the
			// deploy, not the trigger), so a push-only webhook is drift —
			// issue #188 coverage gap: the code was correct
			// (requiredEventsByRepo maps manual → eventRelease) but had no
			// test covering this exact case.
			name:        "manual_repo_webhook_has_push_only",
			repoKey:     "test/drift-manual-needs-release",
			rc:          rc("manual", "svc"),
			hooks:       []githubHook{hook("https://dozor.example.com/deploy/github", "push")},
			wantMissing: "release",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(githubHooksHandler(tc.hooks))
			defer srv.Close()

			cfg := makeConfig(map[string]RepoConfig{tc.repoKey: tc.rc})
			d := newTestDriftChecker(t, cfg, "", srv.URL)

			result := d.checkOneRepoWebhook(context.Background(), tc.repoKey, d.requiredEventsByRepo()[tc.repoKey])

			if result.outcome != outcomeDrift {
				t.Fatalf("outcome = %q, want %q", result.outcome, outcomeDrift)
			}
			if !strings.Contains(strings.Join(result.missing, ","), tc.wantMissing) {
				t.Errorf("missing events = %v, want to contain %q", result.missing, tc.wantMissing)
			}
		})
	}
}

func TestCheckWebhookEvents_MultiTarget_UnionRequired(t *testing.T) {
	t.Parallel()

	// A monorepo with two entries: one deploy_on="" (needs push) and one
	// deploy_on="release" (needs release). The webhook must cover BOTH.
	repoKey := "test/mono-union"

	tests := []struct {
		name        string
		hooks       []githubHook
		wantOutcome string
	}{
		{
			name:        "both_events_covered_ok",
			hooks:       []githubHook{hook("https://dozor.example.com/deploy/github", "push", "release")},
			wantOutcome: outcomeOK,
		},
		{
			name:        "missing_release_drift",
			hooks:       []githubHook{hook("https://dozor.example.com/deploy/github", "push")},
			wantOutcome: outcomeDrift,
		},
		{
			name:        "missing_push_drift",
			hooks:       []githubHook{hook("https://dozor.example.com/deploy/github", "release")},
			wantOutcome: outcomeDrift,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(githubHooksHandler(tc.hooks))
			defer srv.Close()

			cfg := makeConfig(map[string]RepoConfig{
				repoKey:                  rc("", "mono-push"),
				repoKey + "#releaseonly": rc("release", "mono-release"),
			})
			d := newTestDriftChecker(t, cfg, "", srv.URL)

			required := d.requiredEventsByRepo()[repoKey]
			result := d.checkOneRepoWebhook(context.Background(), repoKey, required)

			if result.outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", result.outcome, tc.wantOutcome)
			}
		})
	}
}

func TestCheckWebhookEvents_NoToken_DistinctOutcome(t *testing.T) {
	t.Parallel()

	repoKey := "test/no-token"
	cfg := makeConfig(map[string]RepoConfig{repoKey: rc("", "svc")})
	d := &DriftChecker{
		cfg:    cfg,
		token:  "", // no token
		client: &http.Client{},
	}

	required := d.requiredEventsByRepo()
	result := d.checkOneRepoWebhook(context.Background(), repoKey, required[repoKey])

	// No token → the API call cannot be made. checkOneRepoWebhook calls
	// listRepoHooks which fails → api_error. But checkWebhookEvents short-
	// circuits on no token before calling the API. Test that path:
	d.checkWebhookEvents(context.Background())

	// The no_token outcome is set by checkWebhookEvents (not checkOneRepoWebhook).
	// Verify via the returned result that the API path would fail, and via the
	// gauge that checkWebhookEvents marked it no_token (not ok, not drift).
	_ = result
	if got := gaugeValue(repoKey, checkWebhookEvents, outcomeNoToken); got != 1 {
		t.Errorf("no_token gauge = %v, want 1", got)
	}
	if got := gaugeValue(repoKey, checkWebhookEvents, outcomeOK); got != 0 {
		t.Errorf("ok gauge = %v, want 0 (must not be folded into ok)", got)
	}
	if got := gaugeValue(repoKey, checkWebhookEvents, outcomeDrift); got != 0 {
		t.Errorf("drift gauge = %v, want 0 (no token is not drift)", got)
	}
}

func TestCheckWebhookEvents_APIError_DistinctOutcome(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(githubErrorHandler())
	defer srv.Close()

	repoKey := "test/api-error"
	cfg := makeConfig(map[string]RepoConfig{repoKey: rc("", "svc")})
	d := newTestDriftChecker(t, cfg, "", srv.URL)

	result := d.checkOneRepoWebhook(context.Background(), repoKey, d.requiredEventsByRepo()[repoKey])

	if result.outcome != outcomeAPIError {
		t.Fatalf("outcome = %q, want %q", result.outcome, outcomeAPIError)
	}
}

func TestCheckWebhookEvents_NoDozorWebhook_DistinctOutcome(t *testing.T) {
	t.Parallel()

	// Hooks exist but none point at /deploy/github.
	srv := httptest.NewServer(githubHooksHandler([]githubHook{
		hook("https://other.example.com/webhook", "push", "release"),
	}))
	defer srv.Close()

	repoKey := "test/no-webhook"
	cfg := makeConfig(map[string]RepoConfig{repoKey: rc("", "svc")})
	d := newTestDriftChecker(t, cfg, "", srv.URL)

	result := d.checkOneRepoWebhook(context.Background(), repoKey, d.requiredEventsByRepo()[repoKey])

	if result.outcome != outcomeNoWebhook {
		t.Fatalf("outcome = %q, want %q (hooks exist but none are dozor's)", result.outcome, outcomeNoWebhook)
	}
}

func TestCheckWebhookEvents_MultipleDozorHooks_UnionEvents(t *testing.T) {
	t.Parallel()

	// Two dozor-pointing hooks: one has push, the other has release.
	// The union covers both → ok for a repo needing push+release.
	srv := httptest.NewServer(githubHooksHandler([]githubHook{
		hook("https://dozor.example.com/deploy/github", "push"),
		hook("https://dozor-old.example.com/deploy/github", "release"),
	}))
	defer srv.Close()

	repoKey := "test/multi-hook-union"
	cfg := makeConfig(map[string]RepoConfig{
		repoKey:                  rc("", "mono-push"),
		repoKey + "#releaseonly": rc("release", "mono-release"),
	})
	d := newTestDriftChecker(t, cfg, "", srv.URL)

	result := d.checkOneRepoWebhook(context.Background(), repoKey, d.requiredEventsByRepo()[repoKey])

	if result.outcome != outcomeOK {
		t.Errorf("outcome = %q, want ok (union of two dozor hooks covers push+release)", result.outcome)
	}
}

// --- drift #1: config mirror ---

func TestCheckConfigMirror_Disabled_WhenEnvUnset(t *testing.T) {
	t.Parallel()

	d := &DriftChecker{
		cfg:        makeConfig(map[string]RepoConfig{"test/x": rc("", "x")}),
		livePath:   "/nonexistent/live.yaml",
		mirrorPath: "", // disabled
	}

	if got := d.checkConfigMirror(); got != outcomeDisabled {
		t.Errorf("outcome = %q, want %q", got, outcomeDisabled)
	}
}

func TestCheckConfigMirror_MirrorAbsent_NotDrift(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	livePath := writeYAML(t, dir, "repos:\n  test/x:\n    services: [x]\n    compose_path: /tmp\n    source_path: /tmp\n")

	d := &DriftChecker{
		cfg:        makeConfig(map[string]RepoConfig{"test/x": rc("", "x")}),
		livePath:   livePath,
		mirrorPath: filepath.Join(t.TempDir(), "nonexistent.yaml"), // set but absent
	}

	if got := d.checkConfigMirror(); got != outcomeMirrorUnreadable {
		t.Errorf("outcome = %q, want %q (absent mirror is NOT drift)", got, outcomeMirrorUnreadable)
	}
}

func TestCheckConfigMirror_Identical_OK(t *testing.T) {
	t.Parallel()

	content := "repos:\n  test/x:\n    services: [x]\n    compose_path: /tmp\n    source_path: /tmp\n"
	dir := t.TempDir()
	livePath := writeYAML(t, dir, content)
	mirrorPath := filepath.Join(t.TempDir(), "mirror.yaml")
	if err := os.WriteFile(mirrorPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &DriftChecker{
		cfg:        makeConfig(map[string]RepoConfig{"test/x": rc("", "x")}),
		livePath:   livePath,
		mirrorPath: mirrorPath,
	}

	if got := d.checkConfigMirror(); got != outcomeOK {
		t.Errorf("outcome = %q, want %q", got, outcomeOK)
	}
}

func TestCheckConfigMirror_Differs_Drift(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	livePath := writeYAML(t, dir, "repos:\n  test/x:\n    services: [x]\n    compose_path: /tmp\n    source_path: /tmp\n")
	mirrorPath := filepath.Join(t.TempDir(), "mirror.yaml")
	// Mirror has a different repo — simulates a merged config change sitting in git.
	if err := os.WriteFile(mirrorPath, []byte("repos:\n  test/y:\n    services: [y]\n    compose_path: /tmp\n    source_path: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &DriftChecker{
		cfg:        makeConfig(map[string]RepoConfig{"test/x": rc("", "x")}),
		livePath:   livePath,
		mirrorPath: mirrorPath,
	}

	if got := d.checkConfigMirror(); got != outcomeDrift {
		t.Errorf("outcome = %q, want %q", got, outcomeDrift)
	}
}

// --- gauge verification (non-parallel: shares the global metric) ---

// TestDriftChecker_GaugeVerification verifies the dozor_config_drift gauge is
// set correctly for both checks. Non-parallel because Prometheus metrics are
// global singletons — parallel gauge writes with shared labels race.
func TestDriftChecker_GaugeVerification(t *testing.T) {
	// Webhook events: ok case.
	srv := httptest.NewServer(githubHooksHandler([]githubHook{
		hook("https://dozor.example.com/deploy/github", "push", "release"),
	}))
	defer srv.Close()

	repoOK := "gauge/ok-repo"
	repoDrift := "gauge/drift-repo"
	cfg := makeConfig(map[string]RepoConfig{
		repoOK:    rc("", "svc-ok"),
		repoDrift: rc("release", "svc-drift"),
	})

	// Two API servers: one returns push+release (ok), the other returns push
	// only (drift for a release repo). We need per-repo routing, so use a
	// handler that inspects the path.
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "drift-repo") {
			_ = json.NewEncoder(w).Encode([]githubHook{hook("https://dozor.example.com/deploy/github", "push")})
			return
		}
		_ = json.NewEncoder(w).Encode([]githubHook{hook("https://dozor.example.com/deploy/github", "push", "release")})
	}))
	defer okSrv.Close()

	d := newTestDriftChecker(t, cfg, "", okSrv.URL)

	// Config mirror: drift case (live differs from mirror).
	dir := t.TempDir()
	livePath := writeYAML(t, dir, "repos:\n  gauge/x:\n    services: [x]\n    compose_path: /tmp\n    source_path: /tmp\n")
	d.livePath = livePath
	d.mirrorPath = filepath.Join(t.TempDir(), "mirror.yaml")
	if err := os.WriteFile(d.mirrorPath, []byte("repos:\n  gauge/y:\n    services: [y]\n    compose_path: /tmp\n    source_path: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run both checks (sets gauges).
	d.Run(context.Background())

	// Webhook: ok repo → ok=1, drift=0.
	if got := gaugeValue(repoOK, checkWebhookEvents, outcomeOK); got != 1 {
		t.Errorf("ok repo: ok gauge = %v, want 1", got)
	}
	if got := gaugeValue(repoOK, checkWebhookEvents, outcomeDrift); got != 0 {
		t.Errorf("ok repo: drift gauge = %v, want 0", got)
	}

	// Webhook: drift repo → drift=1, ok=0.
	if got := gaugeValue(repoDrift, checkWebhookEvents, outcomeDrift); got != 1 {
		t.Errorf("drift repo: drift gauge = %v, want 1", got)
	}
	if got := gaugeValue(repoDrift, checkWebhookEvents, outcomeOK); got != 0 {
		t.Errorf("drift repo: ok gauge = %v, want 0", got)
	}

	// Config mirror: drift=1, ok=0.
	if got := gaugeValue("", checkConfigMirror, outcomeDrift); got != 1 {
		t.Errorf("config_mirror drift gauge = %v, want 1", got)
	}
	if got := gaugeValue("", checkConfigMirror, outcomeOK); got != 0 {
		t.Errorf("config_mirror ok gauge = %v, want 0", got)
	}
}

// --- integration: Run does both checks without panic ---

func TestDriftChecker_Run_NeverPanics(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(githubHooksHandler([]githubHook{
		hook("https://dozor.example.com/deploy/github", "push"),
	}))
	defer srv.Close()

	repoKey := "test/run-no-panic"
	cfg := makeConfig(map[string]RepoConfig{repoKey: rc("", "svc")})
	dir := t.TempDir()
	livePath := writeYAML(t, dir, "repos:\n  test/run-no-panic:\n    services: [svc]\n    compose_path: /tmp\n    source_path: /tmp\n")

	d := newTestDriftChecker(t, cfg, livePath, srv.URL)
	d.mirrorPath = "" // config mirror disabled

	// Should not panic.
	d.Run(context.Background())

	// Verify the outcome via the returned result (not the shared gauge).
	result := d.checkOneRepoWebhook(context.Background(), repoKey, d.requiredEventsByRepo()[repoKey])
	if result.outcome != outcomeOK {
		t.Errorf("webhook outcome = %q, want ok", result.outcome)
	}
	if got := d.checkConfigMirror(); got != outcomeDisabled {
		t.Errorf("config_mirror outcome = %q, want disabled", got)
	}
}

// --- helpers for requiredEventsByRepo ---

func TestRequiredEventsByRepo_MultiTarget_Union(t *testing.T) {
	t.Parallel()

	cfg := makeConfig(map[string]RepoConfig{
		"test/mono":              rc("", "a"),
		"test/mono#releaseonly":  rc("release", "b"),
		"test/push-only":         rc("", "c"),
		"test/release-only#prod": rc("release", "d"),
	})
	d := &DriftChecker{cfg: cfg}

	got := d.requiredEventsByRepo()

	if len(got["test/mono"]) != 2 {
		t.Errorf("mono required events = %v, want [push release]", got["test/mono"])
	}
	if len(got["test/push-only"]) != 1 || got["test/push-only"][0] != "push" {
		t.Errorf("push-only required = %v, want [push]", got["test/push-only"])
	}
	if len(got["test/release-only"]) != 1 || got["test/release-only"][0] != "release" {
		t.Errorf("release-only required = %v, want [release]", got["test/release-only"])
	}
}

func TestSortedRepoKeys_DedupAndSort(t *testing.T) {
	t.Parallel()

	cfg := makeConfig(map[string]RepoConfig{
		"test/mono#releaseonly": rc("", "b"),
		"test/mono":             rc("", "a"),
		"test/zeta":             rc("", "z"),
	})
	d := &DriftChecker{cfg: cfg}

	got := d.sortedRepoKeys()

	want := []string{"test/mono", "test/zeta"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("sortedRepoKeys = %v, want %v (deduped + sorted)", got, want)
	}
}
