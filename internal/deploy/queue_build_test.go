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

func TestExecuteBuild_PortMappingRecoverySuccess(t *testing.T) {
	defer zeroDelays(t)()
	origRunner := cmdRunner
	origOutput := outputRunner
	defer func() {
		cmdRunner = origRunner
		outputRunner = origOutput
	}()

	// cmdRunner: succeed on build, up, and force-recreate
	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		return nil
	}

	// outputRunner controls both checkHealth (compose ps) and verifyPortMapping (compose config).
	// Call sequence per checkHealth invocation: (1) compose ps, (2) compose config.
	callCount := 0
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		callCount++
		// Calls 1+2: first checkHealth — ps returns running/no publishers, config declares ports
		// Calls 3+4: second checkHealth (after recovery) — ps returns running/bound port, config declares ports
		switch callCount {
		case 1: // first ps: no publishers
			return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
		case 2: // first config: declares ports so verifyPortMapping triggers error
			return []byte(`{"services":{"svc":{"ports":["8080:8080"]}}}`), nil
		case 3: // second ps: publisher bound
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		default: // second config: still declares ports but we pass since publisher is bound
			return []byte(`{"services":{"svc":{"ports":["8080:8080"]}}}`), nil
		}
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: "/tmp",
			Services:    []string{"svc"},
		},
	}

	result := q.executeBuild(ctx, req)

	if !result.Success {
		t.Fatalf("expected success after port recovery, got error: %s", result.Error)
	}
	if strings.Contains(result.Error, "port") {
		t.Errorf("unexpected port error: %s", result.Error)
	}
}

func TestExecuteBuild_PortMappingRecoveryFails(t *testing.T) {
	defer zeroDelays(t)()
	origRunner := cmdRunner
	origOutput := outputRunner
	defer func() {
		cmdRunner = origRunner
		outputRunner = origOutput
	}()

	// cmdRunner: succeed on build and first up; fail on the recovery force-recreate (second up call)
	upCalls := 0
	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 1 && args[1] == "up" {
			upCalls++
			if upCalls > 1 { // second up = recovery force-recreate
				return errors.New("recreate failed")
			}
		}
		return nil
	}

	// outputRunner: ps returns running/no publishers; config declares ports → triggers port mapping error
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "ps" {
			return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
		}
		// compose config response
		return []byte(`{"services":{"svc":{"ports":["8080:8080"]}}}`), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: "/tmp",
			Services:    []string{"svc"},
		},
	}

	result := q.executeBuild(ctx, req)

	if result.Success {
		t.Fatal("expected failure when force-recreate fails")
	}
	if !strings.Contains(result.Error, "port recovery") {
		t.Errorf("expected 'port recovery' in error, got: %s", result.Error)
	}
	if !strings.Contains(result.Error, "recreate failed") {
		t.Errorf("expected 'recreate failed' in error, got: %s", result.Error)
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
