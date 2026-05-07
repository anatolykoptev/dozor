package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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

	// upRunner: initial compose up succeeds (zeroDelays stubs it to nil, but we override here
	// to be explicit). Port mapping recovery uses runCmd → cmdRunner, not upRunner.
	// So we leave upRunner as the zeroDelays no-op and only fail the cmdRunner recovery call.
	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 1 && args[1] == "up" {
			// This is the port-mapping recovery force-recreate (not the initial up).
			return errors.New("recreate failed")
		}
		return nil
	}

	// outputRunner: ps returns running/no publishers; config declares ports → triggers port mapping error
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "ps" {
			return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
		}
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
