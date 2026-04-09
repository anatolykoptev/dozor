package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "deploy-repos.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/ox-browser:
    compose_path: /home/krolik/deploy/krolik-server
    source_path: /home/krolik/src/ox-browser
    services: [ox-browser]
  anatolykoptev/ox-codes:
    compose_path: /home/krolik/deploy/krolik-server
    source_path: /home/krolik/src/ox-codes
    services: [ox-codes]
    no_cache: true
`
	path := writeYAML(t, t.TempDir(), yaml)

	t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", "test-secret-123")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(cfg.Repos))
	}

	if cfg.Secret != "test-secret-123" {
		t.Errorf("secret = %q, want %q", cfg.Secret, "test-secret-123")
	}

	ox := cfg.Repos["anatolykoptev/ox-browser"]
	if ox.ComposePath != "/home/krolik/deploy/krolik-server" {
		t.Errorf("compose_path = %q", ox.ComposePath)
	}
	if ox.SourcePath != "/home/krolik/src/ox-browser" {
		t.Errorf("source_path = %q", ox.SourcePath)
	}
	if len(ox.Services) != 1 || ox.Services[0] != "ox-browser" {
		t.Errorf("services = %v", ox.Services)
	}
	if ox.NoCache {
		t.Error("ox-browser no_cache should be false")
	}

	codes := cfg.Repos["anatolykoptev/ox-codes"]
	if !codes.NoCache {
		t.Error("ox-codes no_cache should be true")
	}
}

func TestLoadConfig_Empty(t *testing.T) {
	yaml := `repos: {}`
	path := writeYAML(t, t.TempDir(), yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty repos")
	}
	if !strings.Contains(err.Error(), "no repos") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_NoServices(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/ox-browser:
    compose_path: /home/krolik/deploy/krolik-server
    services: []
`
	path := writeYAML(t, t.TempDir(), yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for no services")
	}
	if !strings.Contains(err.Error(), "no services") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_NoComposePath(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/ox-browser:
    services: [ox-browser]
`
	path := writeYAML(t, t.TempDir(), yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for no compose_path")
	}
	if !strings.Contains(err.Error(), "no compose_path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/deploy-repos.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "read deploy config") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConfig_Lookup(t *testing.T) {
	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/ox-browser": {
				ComposePath: "/deploy",
				Services:    []string{"ox-browser"},
				SourcePath:  "/src/ox-browser",
			},
		},
	}

	// Found case
	rc := cfg.Lookup("anatolykoptev/ox-browser")
	if rc == nil {
		t.Fatal("expected non-nil for existing repo")
	}
	if rc.ComposePath != "/deploy" {
		t.Errorf("compose_path = %q", rc.ComposePath)
	}

	// Not found case
	rc = cfg.Lookup("anatolykoptev/nonexistent")
	if rc != nil {
		t.Error("expected nil for nonexistent repo")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	// With DOZOR_WORKSPACE
	t.Setenv("DOZOR_WORKSPACE", "/custom/workspace")
	got := DefaultConfigPath()
	want := "/custom/workspace/deploy-repos.yaml"
	if got != want {
		t.Errorf("with DOZOR_WORKSPACE: got %q, want %q", got, want)
	}

	// Without DOZOR_WORKSPACE
	t.Setenv("DOZOR_WORKSPACE", "")
	got = DefaultConfigPath()
	home, _ := os.UserHomeDir()
	want = filepath.Join(home, ".dozor", "deploy-repos.yaml")
	if got != want {
		t.Errorf("without DOZOR_WORKSPACE: got %q, want %q", got, want)
	}
}
