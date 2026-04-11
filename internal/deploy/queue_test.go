package deploy

import (
	"context"
	"errors"
	"strings"
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

// makeReq returns a minimal BuildRequest for retry tests.
// SourcePath is empty to skip git steps; ComposePath is set to trigger docker steps.
func makeReq(composePath string) BuildRequest {
	return BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: composePath,
			Services:    []string{"svc"},
		},
	}
}

// zeroDelays sets healthWait and upRetryDelay to zero for fast tests and
// returns a restore function to be called via defer.
func zeroDelays(t *testing.T) func() {
	t.Helper()
	origHealth := healthWait
	origRetry := upRetryDelay
	healthWait = 0
	upRetryDelay = 0
	return func() {
		healthWait = origHealth
		upRetryDelay = origRetry
	}
}

func TestExecuteBuild_RetryThenSuccess(t *testing.T) {
	defer zeroDelays(t)()
	origRunner := cmdRunner
	defer func() { cmdRunner = origRunner }()

	calls := 0
	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 1 && args[1] == "up" {
			calls++
			if calls == 1 {
				return errors.New("transient error")
			}
			return nil
		}
		// Succeed on build; let compose ps return empty (health check fails — that's fine).
		return nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if calls != 2 {
		t.Fatalf("expected 2 docker up calls (retry), got %d", calls)
	}
	// Must have passed the up step; any remaining error is health/smoke, not up.
	if strings.Contains(result.Error, "docker up") {
		t.Errorf("expected to pass docker up step, got error: %s", result.Error)
	}
}

func TestExecuteBuild_AllRetriesFail(t *testing.T) {
	defer zeroDelays(t)()
	origRunner := cmdRunner
	defer func() { cmdRunner = origRunner }()

	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 1 && args[1] == "up" {
			return errors.New("permanent failure")
		}
		return nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure")
	}
	want := "after 2 attempts"
	if !strings.Contains(result.Error, want) {
		t.Errorf("expected error to contain %q, got: %s", want, result.Error)
	}
}

func TestExecuteBuild_ContextCancelledDuringRetry(t *testing.T) {
	defer zeroDelays(t)()
	origRunner := cmdRunner
	defer func() { cmdRunner = origRunner }()

	ctx, cancel := context.WithCancel(context.Background())

	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 1 && args[1] == "up" {
			// Cancel context so the retry select hits ctx.Done immediately.
			cancel()
			return errors.New("up failed")
		}
		return nil
	}

	q := NewQueue(context.Background(), func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure on context cancellation")
	}
	want := "context cancelled during retry"
	if !strings.Contains(result.Error, want) {
		t.Errorf("expected error to contain %q, got: %s", want, result.Error)
	}
}
