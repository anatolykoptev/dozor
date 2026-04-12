package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// makeOutputRunner returns an outputRunner stub that returns the given JSON bytes
// for "docker compose ps" calls and the given cfgJSON for "docker compose config" calls.
func makeOutputRunner(psJSON, cfgJSON []byte) func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	return func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		// Detect config call by presence of "config" in args.
		for _, a := range args {
			if a == "config" {
				return cfgJSON, nil
			}
		}
		return psJSON, nil
	}
}

func marshalPS(state string, publishers []portPublisher) []byte {
	arr := []containerInfo{{State: state, Status: state, Publishers: publishers}}
	b, _ := json.Marshal(arr)
	return b
}

func marshalCfg(service string, ports []any) []byte {
	type svcDef struct {
		Ports []any `json:"ports"`
	}
	cfg := map[string]any{
		"services": map[string]any{
			service: svcDef{Ports: ports},
		},
	}
	b, _ := json.Marshal(cfg)
	return b
}

// TestCheckHealth_RunningWithBoundPort: state=running, port bound → nil.
func TestCheckHealth_RunningWithBoundPort(t *testing.T) {
	orig := outputRunner
	defer func() { outputRunner = orig }()

	publishers := []portPublisher{{PublishedPort: 8080, TargetPort: 8080, Protocol: "tcp"}}
	outputRunner = makeOutputRunner(
		marshalPS("running", publishers),
		marshalCfg("svc", []any{"8080:8080"}),
	)

	if err := checkHealth(context.Background(), "/tmp", "svc"); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

// TestCheckHealth_RunningPortDeclaredButUnbound: port declared in config, no bound publishers → port mapping error.
func TestCheckHealth_RunningPortDeclaredButUnbound(t *testing.T) {
	orig := outputRunner
	defer func() { outputRunner = orig }()

	outputRunner = makeOutputRunner(
		marshalPS("running", nil), // no publishers
		marshalCfg("svc", []any{"8080:8080"}),
	)

	err := checkHealth(context.Background(), "/tmp", "svc")
	if err == nil {
		t.Fatal("expected port mapping error, got nil")
	}
	if !strings.Contains(err.Error(), "port mapping") {
		t.Errorf("expected 'port mapping' in error, got: %v", err)
	}
}

// TestCheckHealth_RunningNoPortsDeclared: no ports in compose config + empty publishers → nil (no false positive).
func TestCheckHealth_RunningNoPortsDeclared(t *testing.T) {
	orig := outputRunner
	defer func() { outputRunner = orig }()

	outputRunner = makeOutputRunner(
		marshalPS("running", nil),
		marshalCfg("svc", nil), // no ports declared
	)

	if err := checkHealth(context.Background(), "/tmp", "svc"); err != nil {
		t.Fatalf("expected nil for service with no declared ports, got: %v", err)
	}
}

// TestCheckHealth_NotRunning: state=exited → not running error.
func TestCheckHealth_NotRunning(t *testing.T) {
	orig := outputRunner
	defer func() { outputRunner = orig }()

	outputRunner = makeOutputRunner(
		marshalPS("exited", nil),
		marshalCfg("svc", nil),
	)

	err := checkHealth(context.Background(), "/tmp", "svc")
	if err == nil {
		t.Fatal("expected error for non-running container")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("expected 'not running' in error, got: %v", err)
	}
}

// TestCheckHealth_NDJSONFormat: verify NDJSON (non-array) output is also parsed correctly.
func TestCheckHealth_NDJSONFormat(t *testing.T) {
	orig := outputRunner
	defer func() { outputRunner = orig }()

	publishers := []portPublisher{{PublishedPort: 9000, TargetPort: 9000, Protocol: "tcp"}}
	// Single NDJSON line (no wrapping array).
	c := containerInfo{State: "running", Status: "Up 2 minutes", Publishers: publishers}
	b, _ := json.Marshal(c)

	outputRunner = makeOutputRunner(b, marshalCfg("svc", []any{"9000:9000"}))

	if err := checkHealth(context.Background(), "/tmp", "svc"); err != nil {
		t.Fatalf("expected nil for NDJSON format, got: %v", err)
	}
}
