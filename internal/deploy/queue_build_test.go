package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// makeReq returns a minimal BuildRequest for executeBuild tests.
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

// zeroDelays sets healthWait, upRetryDelay, and portRecoveryWait to zero for fast tests and
// returns a restore function to be called via defer.
func zeroDelays(t *testing.T) func() {
	t.Helper()
	origHealth := healthWait
	origRetry := upRetryDelay
	origRecovery := portRecoveryWait
	healthWait = 0
	upRetryDelay = 0
	portRecoveryWait = 0
	return func() {
		healthWait = origHealth
		upRetryDelay = origRetry
		portRecoveryWait = origRecovery
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
		return nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if calls != 2 {
		t.Fatalf("expected 2 docker up calls (retry), got %d", calls)
	}
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
