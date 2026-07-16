package deploy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestLoadConfig_DeployOn_Release_Parses verifies that deploy_on: release
// round-trips through YAML into RepoConfig.DeployOn == "release".
func TestLoadConfig_DeployOn_Release_Parses(t *testing.T) {
	t.Parallel()

	yamlStr := `
repos:
  anatolykoptev/ox-codes:
    compose_path: /tmp
    source_path: /tmp
    services: [ox-codes]
    deploy_on: release
`
	path := writeYAML(t, t.TempDir(), yamlStr)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/ox-codes"]
	if rc.DeployOn != "release" {
		t.Errorf("DeployOn = %q, want %q", rc.DeployOn, "release")
	}
}

// TestLoadConfig_DeployOn_Absent_DefaultsEmpty verifies the default (flag
// absent) is DeployOn == "" — today's push-based behaviour, unchanged.
func TestLoadConfig_DeployOn_Absent_DefaultsEmpty(t *testing.T) {
	t.Parallel()

	yamlStr := `
repos:
  anatolykoptev/ox-codes:
    compose_path: /tmp
    source_path: /tmp
    services: [ox-codes]
`
	path := writeYAML(t, t.TempDir(), yamlStr)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/ox-codes"]
	if rc.DeployOn != "" {
		t.Errorf("DeployOn = %q, want empty (default)", rc.DeployOn)
	}
}

// TestLoadConfig_DeployOn_Bogus_Rejected verifies an unknown deploy_on value
// is rejected at load time with an error naming the repo + the bad value.
func TestLoadConfig_DeployOn_Bogus_Rejected(t *testing.T) {
	t.Parallel()

	yamlStr := `
repos:
  anatolykoptev/ox-codes:
    compose_path: /tmp
    source_path: /tmp
    services: [ox-codes]
    deploy_on: bogus
`
	path := writeYAML(t, t.TempDir(), yamlStr)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for deploy_on: bogus, got nil")
	}
	if !strings.Contains(err.Error(), "ox-codes") {
		t.Errorf("error must name the repo, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error must name the bad value, got: %v", err)
	}
}

// TestHandler_DeployOnRelease_Push_Skipped verifies that a push to a repo
// configured with deploy_on: release does NOT enqueue a build — it is held
// for the release event. RED-on-revert: remove the push filter and this test
// fails (build enqueued, status=queued instead of ignored).
func TestHandler_DeployOnRelease_Push_Skipped(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/ox-codes": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"ox-codes"},
				DeployOn:    "release",
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/ox-codes", "refs/heads/main", "abc1234567890",
		[]string{"src/main.rs"})
	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ignored" {
		t.Errorf("status = %q, want ignored (deploy_on=release holds for release event)", resp["status"])
	}
	if resp["reason"] != "all matched targets are deploy_on: release" {
		t.Errorf("reason = %q, want %q", resp["reason"], "all matched targets are deploy_on: release")
	}
	if q.queuedHas(serviceKey([]string{"ox-codes"})) {
		t.Error("build must NOT be enqueued for deploy_on=release repo on push")
	}
}

// TestHandler_DeployOnDefault_Push_Builds verifies the default (DeployOn="")
// preserves today's push-based behaviour — a push enqueues a build.
func TestHandler_DeployOnDefault_Push_Builds(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/ox-codes": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"ox-codes"},
				DeployOn:    "",
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/ox-codes", "refs/heads/main", "abc1234567890",
		[]string{"src/main.rs"})
	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (default deploy_on builds on push)", resp["status"])
	}
	if !q.queuedHas(serviceKey([]string{"ox-codes"})) {
		t.Error("build must be enqueued for default deploy_on repo on push")
	}
}

// TestHandler_DeployOnRelease_ReleaseEvent_Builds verifies the feature's whole
// point: a release event for a deploy_on=release repo DOES enqueue a build.
// The release path needs no special-casing — it already handles any repo.
func TestHandler_DeployOnRelease_ReleaseEvent_Builds(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/ox-codes": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"ox-codes"},
				DeployOn:    "release",
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := releasePayload("anatolykoptev/ox-codes", "v1.0.0", "main")
	w := postRelease(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (deploy_on=release repo deploys on release event)",
			resp["status"])
	}
	if !q.queuedHas(serviceKey([]string{"ox-codes"})) {
		t.Error("build must be enqueued for deploy_on=release repo on release event")
	}
}

// TestHandler_DeployOn_Monorepo_Mixed_FiltersPerMatch verifies that a
// monorepo push with two matches — one deploy_on=release and one deploy_on=""
// — builds ONLY the "" target, filtering the release one per-match (not
// all-or-nothing).
func TestHandler_DeployOn_Monorepo_Mixed_FiltersPerMatch(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/mono": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"mono-push"},
				DeployOn:    "",
			},
			"anatolykoptev/mono#releaseonly": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"mono-release"},
				DeployOn:    "release",
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/mono", "refs/heads/main", "abc1234567890",
		[]string{"src/main.rs"})
	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Key-sorted order: "anatolykoptev/mono" (push) before "…#releaseonly"
	// (release). The push target builds (queued), the release one is filtered
	// out before the multi-target dispatch loop — so the aggregated status is
	// a single "queued" (the release target never reaches the status list).
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (only the push target builds)", resp["status"])
	}
	if !q.queuedHas(serviceKey([]string{"mono-push"})) {
		t.Error("mono-push (deploy_on=\"\") must be enqueued")
	}
	if q.queuedHas(serviceKey([]string{"mono-release"})) {
		t.Error("mono-release (deploy_on=release) must NOT be enqueued on push")
	}
}
