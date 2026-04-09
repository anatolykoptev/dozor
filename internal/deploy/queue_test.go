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

	// Block the worker by filling it with a request that takes time.
	// We mark a key as "building" manually to test dedup.
	q.mu.Lock()
	q.building["ox-browser"] = true
	q.mu.Unlock()

	req := BuildRequest{
		Repo:      "anatolykoptev/ox-browser",
		CommitSHA: "abc1234",
		Config: RepoConfig{
			ComposePath: "/tmp",
			Services:    []string{"ox-browser"},
		},
	}

	ok := q.Submit(req)
	if ok {
		t.Fatal("expected Submit to return false (deduplicated by building)")
	}

	// Also test dedup by queued flag
	q.mu.Lock()
	delete(q.building, "ox-browser")
	q.queued["ox-browser"] = true
	q.mu.Unlock()

	ok = q.Submit(req)
	if ok {
		t.Fatal("expected Submit to return false (deduplicated by queued)")
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
