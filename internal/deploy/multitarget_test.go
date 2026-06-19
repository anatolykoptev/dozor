package deploy

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestConfig_LookupAll covers the multi-target resolver that backs monorepos
// with several independent deploy targets keyed "owner/repo#<suffix>".
func TestConfig_LookupAll(t *testing.T) {
	t.Parallel()

	cfg := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/piter-now":       {SourcePath: "/a", Services: []string{"piter-now"}},
		"anatolykoptev/piter-now#hully": {SourcePath: "/b", Services: []string{"hully-web"}},
		"anatolykoptev/other":           {SourcePath: "/c", Services: []string{"other"}},
	}}

	// Both piter-now targets match, in deterministic key-sorted order
	// ("…piter-now" sorts before "…piter-now#hully").
	got := cfg.LookupAll("anatolykoptev/piter-now", "main")
	if len(got) != 2 {
		t.Fatalf("LookupAll(piter-now) = %d targets, want 2", len(got))
	}
	if got[0].Services[0] != "piter-now" || got[1].Services[0] != "hully-web" {
		t.Errorf("order = [%s,%s], want [piter-now,hully-web]",
			got[0].Services[0], got[1].Services[0])
	}

	// A single-target repo returns exactly one (parity with LookupBranch).
	if n := len(cfg.LookupAll("anatolykoptev/other", "main")); n != 1 {
		t.Errorf("single-target = %d, want 1", n)
	}
	// Unknown repo returns none.
	if n := len(cfg.LookupAll("anatolykoptev/nope", "main")); n != 0 {
		t.Errorf("unknown repo = %d, want 0", n)
	}

	// Branch filtering: same repo, two branches → each branch resolves to one.
	cfg2 := &Config{Repos: map[string]RepoConfig{
		"anatolykoptev/repo":     {SourcePath: "/a", Branch: "main"},
		"anatolykoptev/repo#dev": {SourcePath: "/b", Branch: "dev"},
	}}
	if n := len(cfg2.LookupAll("anatolykoptev/repo", "main")); n != 1 {
		t.Errorf("branch=main = %d, want 1", n)
	}
	if n := len(cfg2.LookupAll("anatolykoptev/repo", "dev")); n != 1 {
		t.Errorf("branch=dev = %d, want 1", n)
	}
	// branch == "" (release path) matches every entry of the repo.
	if n := len(cfg2.LookupAll("anatolykoptev/repo", "")); n != 2 {
		t.Errorf("branch=any = %d, want 2", n)
	}
}

// TestHandler_MultiTarget_DispatchesIndependently verifies that a push to a
// monorepo with two deploy targets dispatches only the target(s) whose
// BuildPaths the change matched, and aggregates per-target statuses.
func TestHandler_MultiTarget_DispatchesIndependently(t *testing.T) {
	t.Parallel()

	newHandler := func() *Handler {
		cfg := &Config{Repos: map[string]RepoConfig{
			"anatolykoptev/piter-now": {
				ComposePath: "/tmp", SourcePath: "/tmp",
				Services:   []string{"piter-now"},
				BuildPaths: []string{"apps/piter/**", "packages/core/**"},
			},
			"anatolykoptev/piter-now#hully": {
				ComposePath: "/tmp", SourcePath: "/tmp",
				Services:   []string{"hully-web"},
				BuildPaths: []string{"apps/hully/**", "packages/core/**"},
			},
		}}
		q, _ := newTestQueue()
		return NewHandler(cfg, q, func(string) {})
	}

	statusFor := func(t *testing.T, files []string) string {
		t.Helper()
		h := newHandler()
		defer h.Close()
		body := pushPayloadWithFiles("anatolykoptev/piter-now", "refs/heads/main",
			"abc1234567890", files)
		w := postPush(h, body)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d", w.Code)
		}
		var resp map[string]string
		_ = json.NewDecoder(w.Body).Decode(&resp)
		return resp["status"]
	}

	// Order is key-sorted: piter-now first, hully second.
	if s := statusFor(t, []string{"apps/hully/src/index.astro"}); s != "skipped,queued" {
		t.Errorf("hully-only push status=%q, want skipped,queued", s)
	}
	if s := statusFor(t, []string{"apps/piter/src/pages/index.astro"}); s != "queued,skipped" {
		t.Errorf("piter-only push status=%q, want queued,skipped", s)
	}
	// A shared dependency (packages/core) deploys BOTH targets.
	if s := statusFor(t, []string{"packages/core/src/content/types.ts"}); s != "queued,queued" {
		t.Errorf("shared-core push status=%q, want queued,queued", s)
	}
}
