package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// multiBranchConfig returns a Config with two entries for the same GitHub repo:
//   - "owner/oxpulse-chat"        (branch: main,  service: oxpulse-chat)
//   - "owner/oxpulse-chat#dev"    (branch: dev,   service: oxpulse-chat-staging)
//
// The YAML map keys use the "#branch" suffix convention for disambiguation.
func multiBranchConfig() *Config {
	return &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/oxpulse-chat": {
				ComposePath: "/home/krolik/deploy/krolik-server",
				SourcePath:  "/home/krolik/src/oxpulse-chat",
				Services:    []string{"oxpulse-chat"},
				// Branch defaults to "main" (empty = main in LookupBranch).
			},
			"anatolykoptev/oxpulse-chat#dev": {
				ComposePath: "/home/krolik/deploy/krolik-server",
				SourcePath:  "/home/krolik/src/oxpulse-chat",
				Services:    []string{"oxpulse-chat-staging"},
				Branch:      "dev",
			},
		},
	}
}

// TestMultiBranch_PushToMain_TriggersMainEntry verifies that a push to "main"
// is dispatched to the "oxpulse-chat" (prod) service, not the staging service.
func TestMultiBranch_PushToMain_TriggersMainEntry(t *testing.T) {
	t.Parallel()

	cfg := multiBranchConfig()
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayload("anatolykoptev/oxpulse-chat", "refs/heads/main", "abc1234567890")
	w := postMultiBranchPush(h, body)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (main push should dispatch prod)", resp["status"])
	}

	// Verify the queued build targets the prod service, not staging.
	// queuedHas uses serviceKey(services) as the map key.
	if !q.queuedHas(serviceKey([]string{"oxpulse-chat"})) {
		t.Fatal("expected build pending for 'oxpulse-chat', got none")
	}
	// Staging service must NOT be queued.
	if q.queuedHas(serviceKey([]string{"oxpulse-chat-staging"})) {
		t.Error("staging service must not be queued on a main branch push")
	}
}

// TestMultiBranch_PushToDev_TriggersDevEntry verifies that a push to "dev"
// is dispatched to the "oxpulse-chat-staging" service.
func TestMultiBranch_PushToDev_TriggersDevEntry(t *testing.T) {
	t.Parallel()

	cfg := multiBranchConfig()
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayload("anatolykoptev/oxpulse-chat", "refs/heads/dev", "def5678abcdef")
	w := postMultiBranchPush(h, body)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (dev push should dispatch staging)", resp["status"])
	}

	// Verify the queued build targets the staging service.
	if !q.queuedHas(serviceKey([]string{"oxpulse-chat-staging"})) {
		t.Fatal("expected build pending for 'oxpulse-chat-staging', got none")
	}
	// Prod service must NOT be queued.
	if q.queuedHas(serviceKey([]string{"oxpulse-chat"})) {
		t.Error("prod service must not be queued on a dev branch push")
	}
}

// TestMultiBranch_PushToFeature_IsIgnored verifies that a push to an unconfigured
// branch is ignored (same behaviour as before for unknown repos).
func TestMultiBranch_PushToFeature_IsIgnored(t *testing.T) {
	t.Parallel()

	cfg := multiBranchConfig()
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayload("anatolykoptev/oxpulse-chat", "refs/heads/feature/new-ui", "aaa0001")
	w := postMultiBranchPush(h, body)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Errorf("status = %q, want ignored for unconfigured branch", resp["status"])
	}
}

// TestMultiBranch_DebounceIsolation verifies that concurrent pushes to "main" and
// "dev" within the debounce window do NOT collapse into a single build — each
// branch maintains an independent debounce timer.
func TestMultiBranch_DebounceIsolation(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(time.Unix(1_700_000_000, 0))

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/oxpulse-chat": {
				ComposePath:     "/tmp",
				SourcePath:      "/tmp",
				Services:        []string{"oxpulse-chat"},
				DebounceSeconds: 60,
			},
			"anatolykoptev/oxpulse-chat#dev": {
				ComposePath:     "/tmp",
				SourcePath:      "/tmp",
				Services:        []string{"oxpulse-chat-staging"},
				Branch:          "dev",
				DebounceSeconds: 60,
			},
		},
	}
	q, _ := newTestQueue()

	h := &Handler{config: cfg, queue: q, notify: func(string) {}}
	var mu sync.Mutex
	var dispatched []PendingEvent
	h.debouncer = NewDebouncer(clock, func(ev PendingEvent) {
		mu.Lock()
		dispatched = append(dispatched, ev)
		mu.Unlock()
		h.dispatch(ev)
	})
	defer h.Close()

	// Push to main at t=0.
	mainBody := pushPayload("anatolykoptev/oxpulse-chat", "refs/heads/main", "main-sha1")
	wMain := postMultiBranchPush(h, mainBody)
	if wMain.Code != 200 {
		t.Fatalf("main push status = %d", wMain.Code)
	}
	var rMain map[string]string
	_ = json.NewDecoder(wMain.Body).Decode(&rMain)
	if rMain["status"] != "debounced" {
		t.Errorf("main push: status = %q, want debounced", rMain["status"])
	}

	// Push to dev at t=5s (within main's debounce window).
	clock.Advance(5 * time.Second)
	devBody := pushPayload("anatolykoptev/oxpulse-chat", "refs/heads/dev", "dev-sha1")
	wDev := postMultiBranchPush(h, devBody)
	if wDev.Code != 200 {
		t.Fatalf("dev push status = %d", wDev.Code)
	}
	var rDev map[string]string
	_ = json.NewDecoder(wDev.Body).Decode(&rDev)
	if rDev["status"] != "debounced" {
		t.Errorf("dev push: status = %q, want debounced", rDev["status"])
	}

	// Debouncer should have 2 pending entries — one per branch.
	if got := h.debouncer.Pending(); got != 2 {
		t.Errorf("debouncer pending = %d, want 2 (main and dev must be independent)", got)
	}

	// Advance past both debounce windows — both should fire independently.
	clock.Advance(61 * time.Second)

	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(dispatched) == 2 },
		2*time.Second, "two independent dispatches after debounce window")

	mu.Lock()
	defer mu.Unlock()

	svcs := make(map[string]bool)
	for _, ev := range dispatched {
		for _, svc := range ev.Config.Services {
			svcs[svc] = true
		}
	}
	if !svcs["oxpulse-chat"] {
		t.Error("oxpulse-chat (prod) must be dispatched")
	}
	if !svcs["oxpulse-chat-staging"] {
		t.Error("oxpulse-chat-staging (staging) must be dispatched")
	}
}

// TestMultiBranch_BackwardCompat_SingleEntry verifies that existing single-entry
// configs (no branch: field) continue to work as before — push to main triggers deploy.
func TestMultiBranch_BackwardCompat_SingleEntry(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/go-job": {
				ComposePath: "/home/krolik/deploy/krolik-server",
				SourcePath:  "/home/krolik/src/go-job",
				Services:    []string{"go-job"},
				// No Branch field — defaults to "main".
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayload("anatolykoptev/go-job", "refs/heads/main", "oldstyle123")
	w := postMultiBranchPush(h, body)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (backward-compat single-entry)", resp["status"])
	}
}

// TestLookupBranch_MultiEntry verifies the LookupBranch logic directly without HTTP.
func TestLookupBranch_MultiEntry(t *testing.T) {
	t.Parallel()

	cfg := multiBranchConfig()

	// "main" → prod entry (no Branch field set, defaults to "main").
	rc := cfg.LookupBranch("anatolykoptev/oxpulse-chat", "main")
	if rc == nil {
		t.Fatal("LookupBranch(main): got nil")
	}
	if len(rc.Services) == 0 || rc.Services[0] != "oxpulse-chat" {
		t.Errorf("main entry services = %v, want [oxpulse-chat]", rc.Services)
	}

	// "dev" → staging entry.
	rc = cfg.LookupBranch("anatolykoptev/oxpulse-chat", "dev")
	if rc == nil {
		t.Fatal("LookupBranch(dev): got nil")
	}
	if len(rc.Services) == 0 || rc.Services[0] != "oxpulse-chat-staging" {
		t.Errorf("dev entry services = %v, want [oxpulse-chat-staging]", rc.Services)
	}

	// Unknown branch → nil.
	rc = cfg.LookupBranch("anatolykoptev/oxpulse-chat", "feature/foo")
	if rc != nil {
		t.Errorf("LookupBranch(feature/foo): got %+v, want nil", rc)
	}

	// Unknown repo → nil.
	rc = cfg.LookupBranch("unknown/repo", "main")
	if rc != nil {
		t.Errorf("LookupBranch(unknown/repo, main): got %+v, want nil", rc)
	}
}

// postMultiBranchPush is a thin wrapper used by multi-branch tests.
func postMultiBranchPush(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/deploy/github",
		strings.NewReader(body),
	)
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
