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

func TestLoadConfig_ProfileGoFlat_NoOverrides(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: go-flat
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/svc"]
	want := profileDefaults["go-flat"].BuildPaths
	if len(rc.BuildPaths) != len(want) {
		t.Fatalf("BuildPaths len = %d, want %d (%v)", len(rc.BuildPaths), len(want), rc.BuildPaths)
	}
	for i, p := range want {
		if rc.BuildPaths[i] != p {
			t.Errorf("BuildPaths[%d] = %q, want %q", i, rc.BuildPaths[i], p)
		}
	}
	wantSkip := profileDefaults["go-flat"].SkipPaths
	if len(rc.SkipPaths) != len(wantSkip) {
		t.Errorf("SkipPaths = %v, want %v", rc.SkipPaths, wantSkip)
	}
}

func TestLoadConfig_ProfileGoFlat_BuildExtras(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: go-flat
    build_paths_extra: [migrations/**]
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/svc"]
	defaults := profileDefaults["go-flat"].BuildPaths
	if len(rc.BuildPaths) != len(defaults)+1 {
		t.Fatalf("BuildPaths len = %d, want %d", len(rc.BuildPaths), len(defaults)+1)
	}
	if rc.BuildPaths[len(rc.BuildPaths)-1] != "migrations/**" {
		t.Errorf("last entry = %q, want migrations/**", rc.BuildPaths[len(rc.BuildPaths)-1])
	}
	for i, p := range defaults {
		if rc.BuildPaths[i] != p {
			t.Errorf("BuildPaths[%d] = %q, want %q (defaults must come first)", i, rc.BuildPaths[i], p)
		}
	}
}

func TestLoadConfig_ProfileWithExplicitBuildPaths_Override(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: go-flat
    build_paths: [foo/**]
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/svc"]
	if len(rc.BuildPaths) != 1 || rc.BuildPaths[0] != "foo/**" {
		t.Errorf("BuildPaths = %v, want [foo/**] (explicit overrides profile)", rc.BuildPaths)
	}
	// SkipPaths should still come from profile since not set.
	if len(rc.SkipPaths) == 0 {
		t.Error("SkipPaths empty, want profile defaults")
	}
}

func TestLoadConfig_UnknownProfile_Error(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: go-foo
`
	path := writeYAML(t, t.TempDir(), yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
	if !strings.Contains(err.Error(), "unknown profile") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_ExtrasWithoutProfile_Error(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    build_paths_extra: [migrations/**]
`
	path := writeYAML(t, t.TempDir(), yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for extras without profile")
	}
	if !strings.Contains(err.Error(), "build_paths_extra") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_ProfileRust_SkipExtras(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    profile: rust
    skip_paths_extra: [node_modules/**]
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/svc"]
	defaults := profileDefaults["rust"].SkipPaths
	if len(rc.SkipPaths) != len(defaults)+1 {
		t.Fatalf("SkipPaths len = %d, want %d (%v)", len(rc.SkipPaths), len(defaults)+1, rc.SkipPaths)
	}
	if rc.SkipPaths[len(rc.SkipPaths)-1] != "node_modules/**" {
		t.Errorf("last skip = %q, want node_modules/**", rc.SkipPaths[len(rc.SkipPaths)-1])
	}
}

func TestLoadConfig_NoProfile_BackwardCompat(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/svc:
    compose_path: /tmp
    source_path: /tmp
    services: [svc]
    build_paths: [memdb-go/**, go.mod]
    skip_paths: ["*.md"]
`
	path := writeYAML(t, t.TempDir(), yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos["anatolykoptev/svc"]
	if len(rc.BuildPaths) != 2 || rc.BuildPaths[0] != "memdb-go/**" {
		t.Errorf("BuildPaths = %v, want [memdb-go/** go.mod]", rc.BuildPaths)
	}
	if len(rc.SkipPaths) != 1 || rc.SkipPaths[0] != "*.md" {
		t.Errorf("SkipPaths = %v, want [*.md]", rc.SkipPaths)
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
