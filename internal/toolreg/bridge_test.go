package toolreg

import (
	"testing"
)

// The tool-args → DeployInput field wiring (issue #239 review, MINOR 5):
// asserted directly so a dropped mapping turns RED instead of silently
// falling back to the zero value.

func TestDeployInputFromArgs_MapsAllowStaleConfig(t *testing.T) {
	in := deployInputFromArgs(map[string]any{"allow_stale_config": true})
	if !in.AllowStaleConfig {
		t.Error("allow_stale_config arg must map to DeployInput.AllowStaleConfig")
	}
	in = deployInputFromArgs(map[string]any{})
	if in.AllowStaleConfig {
		t.Error("AllowStaleConfig must default to false")
	}
}

func TestDeployInputFromArgs_MapsBuildAndPull(t *testing.T) {
	in := deployInputFromArgs(map[string]any{"build": false, "pull": true})
	if in.Build == nil || *in.Build {
		t.Error("build=false must map to *DeployInput.Build == false")
	}
	if in.Pull == nil || !*in.Pull {
		t.Error("pull=true must map to *DeployInput.Pull == true")
	}
	// Absent keys stay nil — the handler distinguishes unset from false.
	in = deployInputFromArgs(map[string]any{})
	if in.Build != nil || in.Pull != nil {
		t.Error("absent build/pull must stay nil")
	}
}
