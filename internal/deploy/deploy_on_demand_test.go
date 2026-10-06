package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// deploy_on: on_demand — an entry that is built and deployed ONLY through
// server_deploy. The incident it closes: a pre-release canary configured
// "manual" was also built by the release event (after the tag it exists to
// gate), re-running a 30-45m heavy build and arming DozorReleaseWithheld for
// a commit that was already running.

func TestLoadConfig_DeployOn_OnDemand_Parses(t *testing.T) {
	t.Parallel()

	yamlStr := `
repos:
  anatolykoptev/ox-canary:
    compose_path: /tmp
    source_path: /tmp
    services: [ox-canary]
    deploy_on: on_demand
`
	path := writeYAML(t, t.TempDir(), yamlStr)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Repos["anatolykoptev/ox-canary"].DeployOn; got != deployOnOnDemand {
		t.Errorf("DeployOn = %q, want %q", got, deployOnOnDemand)
	}
}

// RED-on-revert: drop the on_demand branch of the push filter in
// webhook.go and the push reaches Submit (status "deduplicated", not
// "ignored").
func TestHandler_DeployOnOnDemand_Push_Ignored(t *testing.T) {
	t.Parallel()

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/ox-od-push": {
			ComposePath: "/tmp",
			SourcePath:  "/tmp",
			Services:    []string{"ox-od-push"},
			DeployOn:    deployOnOnDemand,
		},
	}}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	w := postPush(h, pushPayloadWithFiles("anatolykoptev/ox-od-push", "refs/heads/main", "abc1234567890",
		[]string{"src/main.rs"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ignored" {
		t.Errorf("status = %q, want ignored", resp["status"])
	}
	if q.queuedHas(serviceKey([]string{"ox-od-push"})) {
		t.Error("deploy_on=on_demand must not build on push")
	}
}

// The live shape: a manual production lane plus an on_demand canary lane on
// the same repo. A release must build production only. RED-on-revert: add
// on_demand to LookupReleaseTargets in config.go and the canary is routed.
func TestHandler_DeployOnOnDemand_ReleaseEvent_RoutesOnlyReleaseLanes(t *testing.T) {
	t.Parallel()

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/ox-od-rel": {
			ComposePath: "/tmp",
			SourcePath:  "/tmp",
			Services:    []string{"ox-od-rel"},
			DeployOn:    deployOnManual,
		},
		"anatolykoptev/ox-od-rel#staging": {
			ComposePath: "/tmp",
			SourcePath:  "/tmp",
			Services:    []string{"ox-od-rel-staging", "ox-od-rel-stagingprod"},
			DeployOn:    deployOnOnDemand,
		},
	}}
	// Routing, not just the Submit backstop: the canary must never be a
	// release target in the first place.
	if targets := cfg.LookupReleaseTargets("anatolykoptev/ox-od-rel"); len(targets) != 1 || targets[0].DeployOn != deployOnManual {
		t.Fatalf("release targets = %d, want only the manual lane", len(targets))
	}

	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	w := postRelease(h, releasePayload("anatolykoptev/ox-od-rel", "v1.0.0", "main"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !q.queuedHas(serviceKey([]string{"ox-od-rel"})) {
		t.Error("the manual production lane must still be built on release")
	}
	if q.queuedHas(serviceKey([]string{"ox-od-rel-staging", "ox-od-rel-stagingprod"})) {
		t.Error("the on_demand canary lane must not be built on release")
	}
}

// Submit is the chokepoint every automatic path funnels through.
// RED-on-revert: delete the on_demand guard at the top of Queue.Submit in
// queue.go and the request is queued.
func TestQueueSubmit_RefusesOnDemand(t *testing.T) {
	t.Parallel()

	q, _ := newTestQueue()
	accepted := q.Submit(BuildRequest{
		Repo:      "anatolykoptev/ox-od-submit",
		CommitSHA: "abc1234567890",
		Config:    RepoConfig{Services: []string{"ox-od-submit"}, DeployOn: deployOnOnDemand},
	})
	if accepted {
		t.Error("Submit accepted an on_demand build")
	}
	if q.queuedHas(serviceKey([]string{"ox-od-submit"})) {
		t.Error("on_demand build was queued")
	}
}

// The boot reconciler compares against a deployed-SHA receipt keyed by bare
// owner/repo — the production lane's receipt, not the canary's. It must not
// re-drive an on_demand lane from it.
func TestReconcileMissedReleases_SkipsOnDemand(t *testing.T) {
	dir := t.TempDir()

	origURL, origLS, origSHA := gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup
	gitRemoteURLRunner = func(context.Context, string) (string, error) { return "fake-url", nil }
	var lsRemoteCalls int
	gitLsRemoteRunner = func(context.Context, string, string, ...string) (string, error) {
		lsRemoteCalls++
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/heads/main\n", nil
	}
	deployedSHALookup = func(string) string { return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }
	t.Cleanup(func() {
		gitRemoteURLRunner, gitLsRemoteRunner, deployedSHALookup = origURL, origLS, origSHA
	})

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/x#staging": {SourcePath: dir, Services: []string{"svc"}, DeployOn: deployOnOnDemand},
	}}
	q, _ := newTestQueue()
	ReconcileMissedReleases(context.Background(), cfg, q)

	if lsRemoteCalls != 0 {
		t.Errorf("reconcile resolved a target for an on_demand lane (%d ls-remote calls)", lsRemoteCalls)
	}
	if q.queuedHas(serviceKey([]string{"svc"})) {
		t.Error("reconcile enqueued an on_demand lane")
	}
}

func TestRequiredEventsByRepo_OnDemandNeedsNoEvent(t *testing.T) {
	t.Parallel()

	cfg := makeConfig(map[string]RepoConfig{
		"test/mixed":         rc("manual", "prod"),
		"test/mixed#staging": rc(deployOnOnDemand, "canary"),
		"test/od-only":       rc(deployOnOnDemand, "svc"),
	})
	got := (&DriftChecker{cfg: cfg}).requiredEventsByRepo()

	if ev := got["test/mixed"]; len(ev) != 1 || ev[0] != eventRelease {
		t.Errorf("mixed required = %v, want [release] (on_demand must not add push)", ev)
	}
	if ev := got["test/od-only"]; len(ev) != 0 {
		t.Errorf("on_demand-only required = %v, want none", ev)
	}
}

// A repo whose every entry is on_demand needs no webhook, so having none is
// not drift. RED-on-revert: remove the len(required)==0 short-circuit in
// checkWebhookEvents and the outcome becomes no_webhook.
func TestCheckWebhookEvents_OnDemandOnly_NoWebhookIsOK(t *testing.T) {
	srv := httptest.NewServer(githubHooksHandler(nil))
	defer srv.Close()

	repoKey := "gauge/od-only"
	cfg := makeConfig(map[string]RepoConfig{repoKey: rc(deployOnOnDemand, "svc")})
	d := newTestDriftChecker(t, cfg, "", srv.URL)
	d.checkWebhookEvents(context.Background())

	if got := gaugeValue(repoKey, checkWebhookEvents, outcomeOK); got != 1 {
		t.Errorf("ok gauge = %v, want 1", got)
	}
	if got := gaugeValue(repoKey, checkWebhookEvents, outcomeNoWebhook); got != 0 {
		t.Errorf("no_webhook gauge = %v, want 0", got)
	}
}

// server_deploy of an on_demand canary must not overwrite the bare-repo
// deployed-SHA receipt the release lane owns — otherwise the next boot
// reconcile sees production "behind its tag" and rebuilds it.
// RED-on-revert: drop the on_demand early return in
// RecordManualDeployReceipt (deployed_sha_persist.go) and the receipt moves.
func TestRecordManualDeployReceipt_OnDemandLeavesReceipt(t *testing.T) {
	ConfigureDeployedSHAPersistence(filepath.Join(t.TempDir(), "deployed.json"))
	t.Cleanup(func() { ConfigureDeployedSHAPersistence("") })

	const prodSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const tipSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	recordDeployedSHA("anatolykoptev/x", prodSHA)

	RecordManualDeployReceipt(ManualDeployRequest{
		Repo:   "anatolykoptev/x#staging",
		Config: RepoConfig{DeployOn: deployOnOnDemand},
	}, tipSHA)
	if got := lookupDeployedSHA("anatolykoptev/x"); got != prodSHA {
		t.Fatalf("on_demand server_deploy moved the receipt to %q", got)
	}

	// The manual production lane still records — the guard is not a blanket no-op.
	RecordManualDeployReceipt(ManualDeployRequest{
		Repo:   "anatolykoptev/x",
		Config: RepoConfig{DeployOn: deployOnManual},
	}, tipSHA)
	if got := lookupDeployedSHA("anatolykoptev/x"); got != tipSHA {
		t.Fatalf("manual server_deploy did not record: receipt = %q", got)
	}
}
