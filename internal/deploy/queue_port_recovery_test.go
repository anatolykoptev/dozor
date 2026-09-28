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

	// outputRunner is dispatched by command signature, not call position —
	// the build-diff snapshot (config --images + image inspect, issue #214)
	// interleaves with checkHealth's ps/config calls and a positional counter
	// would misroute every response after the first snapshot.
	psCalls := 0
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			// build-diff snapshot: tag resolves to a built image
			return []byte("sha256:0000buildtest\n"), nil
		case len(args) >= 3 && args[1] == "config" && args[2] == "--images":
			// compose model: resolved image name for the service
			return []byte("proj-svc:latest\n"), nil
		case len(args) >= 2 && args[1] == "config":
			// verifyPortMapping's `config --format json`: declares ports
			return []byte(`{"services":{"svc":{"ports":["8080:8080"]}}}`), nil
		case len(args) >= 2 && args[1] == "ps":
			psCalls++
			if psCalls == 1 {
				// first checkHealth — running, no publishers
				return []byte(`[{"State":"running","Status":"Up","Publishers":[]}]`), nil
			}
			// post-recovery checkHealth — publisher bound
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		}
		return []byte("{}"), nil
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
