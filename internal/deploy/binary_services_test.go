package deploy

import (
	"reflect"
	"testing"
)

// TestLoadConfig_BinaryServicesFromUserServices verifies that for a
// kind=binary repo without an explicit `services` field, parsing
// auto-populates Services from UserServices. This is required so the
// deploy queue keys non-empty (empty key collides with drainPending's
// "no work" sentinel) and the build log shows the systemd unit names
// being restarted.
func TestLoadConfig_BinaryServicesFromUserServices(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/test-bin:
    kind: binary
    source_path: /home/krolik/src/test-bin
    build_cmd: [make, install]
    user_services:
      - test-bin-orchestrator
      - test-bin-content
`
	path := writeYAML(t, t.TempDir(), yaml)
	t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", "x")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	rc := cfg.Repos["anatolykoptev/test-bin"]
	want := []string{"test-bin-orchestrator", "test-bin-content"}
	if !reflect.DeepEqual(rc.Services, want) {
		t.Errorf("Services = %v, want %v (auto-populated from UserServices)", rc.Services, want)
	}
}

// TestLoadConfig_BinaryServicesExplicit verifies that an explicit
// `services` field on a binary repo is honoured (not overwritten by
// UserServices).
func TestLoadConfig_BinaryServicesExplicit(t *testing.T) {
	yaml := `
repos:
  anatolykoptev/test-bin:
    kind: binary
    source_path: /home/krolik/src/test-bin
    build_cmd: [make, install]
    services: [custom-key]
    user_services:
      - test-bin-orchestrator
`
	path := writeYAML(t, t.TempDir(), yaml)
	t.Setenv("DOZOR_GITHUB_WEBHOOK_SECRET", "x")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	rc := cfg.Repos["anatolykoptev/test-bin"]
	if !reflect.DeepEqual(rc.Services, []string{"custom-key"}) {
		t.Errorf("explicit Services overwritten: got %v", rc.Services)
	}
}
