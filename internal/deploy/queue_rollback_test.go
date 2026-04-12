package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// psRunning is a valid `docker compose ps --format json` response for a running container.
const psRunning = `[{"State":"running","Status":"Up","Publishers":[]}]`

// TestRollback_HealthCheckFail_RollbackSucceeds verifies that a health-check failure
// triggers rollback and sets RolledBack=true.
func TestRollback_HealthCheckFail_RollbackSucceeds(t *testing.T) {
	defer zeroDelays(t)()
	origCmd := cmdRunner
	origOut := outputRunner
	defer func() { cmdRunner = origCmd; outputRunner = origOut }()

	// compose ps returns "exited" → health check fails.
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "ps" {
			return []byte(`[{"State":"exited","Status":"Exited (1)","Publishers":[]}]`), nil
		}
		if len(args) >= 2 && args[1] == "images" {
			return []byte(`[{"ID":"aabbcc1234567","ContainerName":"svc","Repository":"myrepo","Tag":"latest"}]`), nil
		}
		return []byte("{}"), nil
	}
	cmdRunner = func(_ context.Context, _ string, _ string, _ ...string) error { return nil }

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure due to health check")
	}
	if !result.RolledBack {
		t.Errorf("expected RolledBack=true, got false; error: %s", result.Error)
	}
	if !strings.Contains(result.Error, "rolled back") {
		t.Errorf("expected error to mention rollback, got: %s", result.Error)
	}
}

// TestRollback_RollbackAlsoFails verifies the error suffix when rollback itself fails.
// It calls tryRollback directly with a pre-populated PreviousImages so the test is
// independent of the composeImageID exec path (which bypasses outputRunner).
func TestRollback_RollbackAlsoFails(t *testing.T) {
	origOut := outputRunner
	defer func() { outputRunner = origOut }()

	// composeImageName (via outputRunner) returns empty → "cannot determine image name"
	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "images" {
			return []byte(`[]`), nil
		}
		return []byte("{}"), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := BuildResult{
		Repo:     "test/repo",
		Services: []string{"svc"},
		Error:    "health check svc: exited",
		PreviousImages: map[string]string{
			"svc": "previmg1234567",
		},
	}
	q.tryRollback(ctx, &result, "/tmp")

	if result.RolledBack {
		t.Error("expected RolledBack=false when rollback itself fails")
	}
	if !strings.Contains(result.Error, "rollback also failed") {
		t.Errorf("expected 'rollback also failed' in error, got: %s", result.Error)
	}
}

// TestRollback_ComposeUpFail_RollbackAttempted verifies rollback is triggered on compose up failure.
func TestRollback_ComposeUpFail_RollbackAttempted(t *testing.T) {
	defer zeroDelays(t)()
	origCmd := cmdRunner
	origOut := outputRunner
	defer func() { cmdRunner = origCmd; outputRunner = origOut }()

	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "images" {
			return []byte(`[{"ID":"prev1234567890","ContainerName":"svc","Repository":"myrepo","Tag":"latest"}]`), nil
		}
		return []byte("{}"), nil
	}
	cmdRunner = func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 1 && args[1] == "up" {
			return errors.New("compose up failed")
		}
		return nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure due to compose up error")
	}
	hasRollbackInfo := result.RolledBack || strings.Contains(result.Error, "rollback")
	if !hasRollbackInfo {
		t.Errorf("expected rollback attempt, got error: %s", result.Error)
	}
}

// TestRollback_AllSucceeds_NoRollback verifies RolledBack stays false on clean deploy.
func TestRollback_AllSucceeds_NoRollback(t *testing.T) {
	defer zeroDelays(t)()
	origCmd := cmdRunner
	origOut := outputRunner
	defer func() { cmdRunner = origCmd; outputRunner = origOut }()

	outputRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "ps" {
			return []byte(psRunning), nil
		}
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"services":{}}`), nil
		}
		if len(args) >= 2 && args[1] == "images" {
			return []byte(`[{"ID":"img1234567890","ContainerName":"svc","Repository":"myrepo","Tag":"latest"}]`), nil
		}
		return []byte("{}"), nil
	}
	cmdRunner = func(_ context.Context, _ string, _ string, _ ...string) error { return nil }

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.RolledBack {
		t.Error("expected RolledBack=false on successful deploy")
	}
}
