package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseComposeContexts_SubdirAndRoot verifies that parseComposeContexts
// correctly extracts build.context for every requested service, handling both
// subdirectory contexts (memdb-go case) and root-aligned contexts (go-job case).
func TestParseComposeContexts_SubdirAndRoot(t *testing.T) {
	configJSON := []byte(`{
		"services": {
			"memdb-go": {
				"build": {
					"context": "/home/krolik/src/MemDB/memdb-go",
					"dockerfile": "Dockerfile"
				}
			},
			"go-job": {
				"build": {
					"context": "/home/krolik/src/go-job",
					"dockerfile": "Dockerfile"
				}
			},
			"postgres": {
				"image": "postgres:16"
			}
		}
	}`)

	got, err := parseComposeContexts(configJSON, []string{"memdb-go", "go-job"})
	if err != nil {
		t.Fatalf("parseComposeContexts: unexpected error: %v", err)
	}
	want := map[string]string{
		"memdb-go": "/home/krolik/src/MemDB/memdb-go",
		"go-job":   "/home/krolik/src/go-job",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d contexts, got %d: %+v", len(want), len(got), got)
	}
	for svc, ctx := range want {
		if got[svc] != ctx {
			t.Errorf("service %q: expected context %q, got %q", svc, ctx, got[svc])
		}
	}
}

// TestParseComposeContexts_MissingService returns an error when a requested
// service isn't declared as a build in the compose config.
func TestParseComposeContexts_MissingService(t *testing.T) {
	configJSON := []byte(`{"services": {"postgres": {"image": "postgres:16"}}}`)

	_, err := parseComposeContexts(configJSON, []string{"memdb-go"})
	if err == nil {
		t.Fatal("expected error for missing service, got nil")
	}
	if !strings.Contains(err.Error(), "memdb-go") {
		t.Errorf("expected error to mention missing service name, got: %v", err)
	}
}

// TestResolveBuildOverrides_SubdirContext covers the memdb-go regression:
// original context is a subdirectory of sourcePath, and the override must
// preserve that offset under the worktree path.
func TestResolveBuildOverrides_SubdirContext(t *testing.T) {
	origRunner := outputRunner
	defer func() { outputRunner = origRunner }()

	outputRunner = func(_ context.Context, _ string, _ string, _ ...string) ([]byte, error) {
		return []byte(`{
			"services": {
				"svc_sub": {"build": {"context": "/fake/repo/subdir_a"}},
				"svc_root": {"build": {"context": "/fake/repo"}}
			}
		}`), nil
	}

	overrides, err := resolveBuildOverrides(
		context.Background(),
		"/fake/repo/docker-compose.yml",
		"/fake/repo",
		[]string{"svc_sub", "svc_root"},
		"/tmp/wt",
	)
	if err != nil {
		t.Fatalf("resolveBuildOverrides: unexpected error: %v", err)
	}

	byService := make(map[string]string, len(overrides))
	for _, o := range overrides {
		byService[o.Service] = o.Context
	}

	if got, want := byService["svc_sub"], "/tmp/wt/subdir_a"; got != want {
		t.Errorf("svc_sub: expected context %q, got %q", want, got)
	}
	if got, want := byService["svc_root"], "/tmp/wt"; got != want {
		t.Errorf("svc_root: expected context %q, got %q", want, got)
	}
}

// TestResolveBuildOverrides_ContextOutsideSourceErrors ensures the deploy
// fails loudly (rather than silently using a wrong path) when a service's
// build.context lives outside sourcePath.
func TestResolveBuildOverrides_ContextOutsideSourceErrors(t *testing.T) {
	origRunner := outputRunner
	defer func() { outputRunner = origRunner }()

	outputRunner = func(_ context.Context, _ string, _ string, _ ...string) ([]byte, error) {
		return []byte(`{
			"services": {
				"svc_outside": {"build": {"context": "/elsewhere/other-repo"}}
			}
		}`), nil
	}

	_, err := resolveBuildOverrides(
		context.Background(),
		"/fake/repo/docker-compose.yml",
		"/fake/repo",
		[]string{"svc_outside"},
		"/tmp/wt",
	)
	if err == nil {
		t.Fatal("expected error for context outside sourcePath, got nil")
	}
	if !strings.Contains(err.Error(), "svc_outside") {
		t.Errorf("expected error to mention service name, got: %v", err)
	}
}

// TestWriteBuildContextOverride_Format verifies the override YAML is a
// minimal, deterministic docker-compose document with per-service build
// context overrides.
func TestWriteBuildContextOverride_Format(t *testing.T) {
	overrides := []BuildOverride{
		{Service: "memdb-go", Context: "/tmp/wt/memdb-go"},
		{Service: "go-job", Context: "/tmp/wt"},
	}

	path, err := writeBuildContextOverride(overrides)
	if err != nil {
		t.Fatalf("writeBuildContextOverride: unexpected error: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read override file: %v", err)
	}
	got := string(data)

	want := "services:\n" +
		"  memdb-go:\n    build:\n      context: /tmp/wt/memdb-go\n" +
		"  go-job:\n    build:\n      context: /tmp/wt\n"

	if got != want {
		t.Errorf("override content mismatch.\ngot:\n%s\nwant:\n%s", got, want)
	}
	if filepath.Ext(path) != ".yml" {
		t.Errorf("expected .yml extension, got: %s", path)
	}
}
