package deploy

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQueue_Submit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	var msgs []string
	notify := func(msg string) {
		mu.Lock()
		msgs = append(msgs, msg)
		mu.Unlock()
	}

	q := NewQueue(ctx, notify)
	defer q.Close()

	req := BuildRequest{
		Repo:      "anatolykoptev/ox-browser",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: "/tmp/nonexistent",
			Services:    []string{"ox-browser"},
			SourcePath:  "/tmp/nonexistent-src",
		},
	}

	ok := q.Submit(req)
	if !ok {
		t.Fatal("expected Submit to return true")
	}

	// Wait for worker to pick it up (build will fail, that's fine)
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("expected at least one notification")
	}
}

func TestQueue_Deduplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	notify := func(string) {}

	q := NewQueue(ctx, notify)
	defer q.Close()

	req := BuildRequest{
		Repo:      "anatolykoptev/ox-browser",
		CommitSHA: "abc1234",
		Config: RepoConfig{
			ComposePath: "/tmp",
			Services:    []string{"ox-browser"},
		},
	}

	// Simulate currently-building same SHA → real duplicate (e.g. webhook retry).
	q.mu.Lock()
	q.busySHA["ox-browser"] = req.CommitSHA
	q.building["ox-browser"] = true
	q.mu.Unlock()

	if q.Submit(req) {
		t.Fatal("expected Submit to return false (deduplicated by currently-building same SHA)")
	}

	// Same key, DIFFERENT SHA: must be enqueued (newest-wins), not dropped.
	newer := req
	newer.CommitSHA = "def9876"
	if !q.Submit(newer) {
		t.Fatal("expected Submit to return true (different SHA must queue, not dedup)")
	}
	q.mu.Lock()
	got, has := q.pending["ox-browser"]
	q.mu.Unlock()
	if !has || got.CommitSHA != newer.CommitSHA {
		t.Fatalf("expected pending to hold newer SHA %q, got has=%v sha=%q", newer.CommitSHA, has, got.CommitSHA)
	}

	// Also test dedup by pending same-SHA flag.
	q.mu.Lock()
	delete(q.busySHA, "ox-browser")
	delete(q.building, "ox-browser")
	q.pending["ox-browser"] = req
	q.mu.Unlock()

	if q.Submit(req) {
		t.Fatal("expected Submit to return false (deduplicated by pending same SHA)")
	}
}

// TestQueue_NewestWinsCoalescing reproduces the 2026-05-03 dozor dedup bug:
// two webhooks for the SAME service arrive in quick succession with DIFFERENT
// SHAs while a build is in flight. The older code dedup'd the second by
// service name and dropped the newer commit, leaving prod on the stale SHA.
// Correct behaviour: newer SHA replaces older pending, and gets built once
// the in-flight build completes.
func TestQueue_NewestWinsCoalescing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	// Pretend a build is already in flight for SHA-A.
	q.mu.Lock()
	q.busySHA["svc"] = "aaaaaaa"
	q.building["svc"] = true
	q.mu.Unlock()

	older := BuildRequest{
		Repo: "r", CommitSHA: "bbbbbbb",
		Config: RepoConfig{Services: []string{"svc"}},
	}
	newer := BuildRequest{
		Repo: "r", CommitSHA: "ccccccc",
		Config: RepoConfig{Services: []string{"svc"}},
	}

	if !q.Submit(older) {
		t.Fatal("older submit must enqueue (different SHA from in-flight)")
	}
	if !q.Submit(newer) {
		t.Fatal("newer submit must enqueue (replacing older pending)")
	}

	q.mu.Lock()
	got, has := q.pending["svc"]
	q.mu.Unlock()
	if !has {
		t.Fatal("pending must hold one entry, got none")
	}
	if got.CommitSHA != newer.CommitSHA {
		t.Fatalf("pending must be newer SHA %q, got %q (older was not superseded)", newer.CommitSHA, got.CommitSHA)
	}
}

func TestQueue_Close(t *testing.T) {
	ctx := context.Background()
	notify := func(string) {}

	q := NewQueue(ctx, notify)

	done := make(chan struct{})
	go func() {
		q.Close()
		close(done)
	}()

	select {
	case <-done:
		// OK: worker exited
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return within timeout")
	}
}

func TestServiceKey(t *testing.T) {
	tests := []struct {
		name     string
		services []string
		want     string
	}{
		{"single", []string{"go-wp"}, "go-wp"},
		{"multiple", []string{"go-wp", "go-search"}, "go-wp+go-search"},
		{"empty", []string{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serviceKey(tt.services)
			if got != tt.want {
				t.Errorf("serviceKey(%v) = %q, want %q", tt.services, got, tt.want)
			}
		})
	}
}

func TestShort(t *testing.T) {
	tests := []struct {
		name string
		sha  string
		want string
	}{
		{"long", "abc1234567890def", "abc1234"},
		{"exact7", "abc1234", "abc1234"},
		{"short", "abc", "abc"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := short(tt.sha)
			if got != tt.want {
				t.Errorf("short(%q) = %q, want %q", tt.sha, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"over", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}
