package deploy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pushPayloadWithFiles builds a minimal push event JSON that includes a single
// commit with the given changed files in `modified`.
func pushPayloadWithFiles(repo, ref, sha string, modified []string) string {
	type commit struct {
		ID       string   `json:"id"`
		Modified []string `json:"modified"`
	}
	body := struct {
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		HeadCommit struct {
			ID string `json:"id"`
		} `json:"head_commit"`
		Commits []commit `json:"commits"`
	}{}
	body.Ref = ref
	body.Repository.FullName = repo
	body.HeadCommit.ID = sha
	body.Commits = []commit{{ID: sha, Modified: modified}}
	out, _ := json.Marshal(body)
	return string(out)
}

func postPush(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHandler_PathFilter_SkipsIrrelevant(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"memdb-go"},
				BuildPaths:  []string{"memdb-go/**", "go.mod", "go.sum"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/memdb", "refs/heads/main", "abc1234567890",
		[]string{"evaluation/locomo/score.py", "ROADMAP.md"})

	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "skipped" || resp["reason"] != "no_relevant_paths" {
		t.Errorf("response = %+v, want status=skipped reason=no_relevant_paths", resp)
	}
}

func TestHandler_PathFilter_BuildsOnRelevantChange(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"memdb-go"},
				BuildPaths:  []string{"memdb-go/**", "go.mod"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/memdb", "refs/heads/main", "abc1234567890",
		[]string{"memdb-go/internal/handlers/foo.go", "ROADMAP.md"})

	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("response status = %q, want queued", resp["status"])
	}
}

func TestHandler_PathFilter_NoCommitsBypassesFilter(t *testing.T) {
	t.Parallel()
	// Force push or oversize push: GitHub omits commits[]. We must not skip.
	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: "/tmp", SourcePath: "/tmp",
				Services:   []string{"memdb-go"},
				BuildPaths: []string{"memdb-go/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayload("anatolykoptev/memdb", "refs/heads/main", "abc1234567890")

	w := postPush(h, body)
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("status=%q, want queued (filter must bypass when commits[] missing)", resp["status"])
	}
}

func TestHandler_PathFilter_ProfileGoFlat_SkipsDocsOnlyPush(t *testing.T) {
	t.Parallel()

	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: go-flat
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/svc", "refs/heads/main", "abc1234567890",
		[]string{"docs/foo.md"})

	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "skipped" {
		t.Errorf("response = %+v, want status=skipped", resp)
	}
}

func TestHandler_PathFilter_ProfileGoFlat_BuildsOnExtraMatch(t *testing.T) {
	t.Parallel()

	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: go-flat
    build_paths_extra: [migrations/**]
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := pushPayloadWithFiles("anatolykoptev/svc", "refs/heads/main", "abc1234567890",
		[]string{"migrations/0001.sql"})

	w := postPush(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("response = %+v, want status=queued", resp)
	}
}

func TestHandler_DebounceCoalescesBurst(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(time.Unix(1_700_000_000, 0))

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath:     "/tmp", SourcePath: "/tmp",
				Services:        []string{"memdb-go"},
				DebounceSeconds: 60,
			},
		},
	}
	q, _ := newTestQueue()

	// Hand-build the handler so we can inject the fake clock into its debouncer.
	h := &Handler{config: cfg, queue: q, notify: func(string) {}}
	var mu sync.Mutex
	var dispatched []PendingEvent
	h.debouncer = NewDebouncer(clock, func(ev PendingEvent) {
		mu.Lock()
		dispatched = append(dispatched, ev)
		mu.Unlock()
		// Mirror production dispatch into the queue too.
		h.dispatch(ev)
	})
	defer h.Close()

	// Three webhooks in 30s, each with a different HEAD SHA.
	for _, sha := range []string{"aaa1111", "bbb2222", "ccc3333"} {
		body := pushPayloadWithFiles("anatolykoptev/memdb", "refs/heads/main", sha, []string{"x.go"})
		w := postPush(h, body)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var resp map[string]string
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["status"] != "debounced" {
			t.Errorf("status=%q, want debounced", resp["status"])
		}
		clock.Advance(10 * time.Second)
	}

	// Still pending, no dispatch yet.
	mu.Lock()
	if len(dispatched) != 0 {
		mu.Unlock()
		t.Fatalf("dispatched prematurely: %d", len(dispatched))
	}
	mu.Unlock()

	// Advance past quiet window from the LAST event (which was at +30s).
	clock.Advance(61 * time.Second)

	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(dispatched) == 1 },
		2*time.Second, "exactly one dispatch after debounce")

	mu.Lock()
	defer mu.Unlock()
	if got := dispatched[0].CommitSHA; got != "ccc3333" {
		t.Errorf("dispatched commit = %q, want %q (HEAD at fire time)", got, "ccc3333")
	}
	if got := dispatched[0].HitCount; got != 3 {
		t.Errorf("HitCount = %d, want 3", got)
	}
}
