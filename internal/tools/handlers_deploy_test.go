package tools

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/anatolykoptev/dozor/internal/engine"
)

// The DeployInput → ManualDeployRequest field wiring (issue #239 review,
// MINOR 5): each mapping is asserted directly so a dropped assignment turns
// RED instead of silently falling back to the zero value.

func TestManualDeployRequest_MapsAllowStaleConfig(t *testing.T) {
	req, err := manualDeployRequest("o/r", deploy.RepoConfig{ComposePath: "/fake/compose"}, engine.DeployInput{
		AllowStaleConfig: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !req.AllowStaleConfig {
		t.Error("allow_stale_config must map to req.AllowStaleConfig — dropping it silently deploys a verified-claimed config that was never verified")
	}
	if req.Repo != "o/r" {
		t.Errorf("repo mapping broken: %q", req.Repo)
	}
}

func TestManualDeployRequest_BuildFalseMapsNoBuild(t *testing.T) {
	f := false
	req, err := manualDeployRequest("o/r", deploy.RepoConfig{ComposePath: "/fake/compose"}, engine.DeployInput{
		Build: &f,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !req.NoBuild {
		t.Error("build=false must map to req.NoBuild — without it a 'no-build' deploy silently rebuilds")
	}
}

func TestManualDeployRequest_BuildFalseFromDiskRejected(t *testing.T) {
	f := false
	_, err := manualDeployRequest("o/r", deploy.RepoConfig{ComposePath: "/fake/compose"}, engine.DeployInput{
		Build:    &f,
		FromDisk: true,
	})
	if err == nil || !strings.Contains(err.Error(), "from_disk") {
		t.Errorf("build=false + from_disk must be rejected, got %v", err)
	}
}

func TestManualDeployRequest_BuildFalseNonComposeRejected(t *testing.T) {
	f := false
	_, err := manualDeployRequest("o/r", deploy.RepoConfig{Kind: deploy.KindBinary}, engine.DeployInput{
		Build: &f,
	})
	if err == nil || !strings.Contains(err.Error(), "build=false") {
		t.Errorf("build=false on a binary repo must be rejected, got %v", err)
	}
}

func TestManualDeployRequest_BuildTrueLeavesNoBuildFalse(t *testing.T) {
	b := true
	req, err := manualDeployRequest("o/r", deploy.RepoConfig{ComposePath: "/fake/compose"}, engine.DeployInput{
		Build: &b,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.NoBuild {
		t.Error("build=true must not set NoBuild")
	}
}
