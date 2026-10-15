package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/testutil/fakessh"
)

// newRemoteAgent builds an agent whose transport "ssh"es to a fake binary that
// records, rather than runs, every command.
func newRemoteAgent(t *testing.T) (*engine.ServerAgent, func() []string) {
	t.Helper()
	cmds := fakessh.Install(t)
	a := engine.NewAgent(engine.Config{Host: "fake.invalid", SSHPort: 22, Timeout: 5 * time.Second, LogLines: 10})
	t.Cleanup(a.Close)
	return a, cmds
}

func assertNoRaw(t *testing.T, cmds []string, raw string) {
	t.Helper()
	if c, bad := fakessh.Contains(cmds, raw); bad {
		t.Errorf("hostile input %q reached a shell command: %q", raw, c)
	}
}

func TestShellSink_Systemd_HostileNamesRunNothing(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := newRemoteAgent(t)
		out := a.GetSystemdStatus(context.Background(), []string{h})
		assertNoRaw(t, cmds(), h)
		if len(cmds()) != 0 {
			t.Errorf("hostile unit %q: expected no command, got %q (output %q)", h, cmds(), out)
		}
	}
}

func TestShellSink_Systemd_ValidNamesStillRun(t *testing.T) {
	for _, n := range []string{"dozor", "go-wp", "user@1000.service", "nginx.service"} {
		a, cmds := newRemoteAgent(t)
		a.GetSystemdStatus(context.Background(), []string{n})
		if _, ok := fakessh.Contains(cmds(), "systemctl is-active "+n); !ok {
			t.Errorf("valid unit %q must still be queried, commands: %q", n, cmds())
		}
	}
}

func TestShellSink_ContainerLogs_HostileNamesRunNothing(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := newRemoteAgent(t)
		a.GetContainerLogs(context.Background(), h, 10)
		assertNoRaw(t, cmds(), h)
		if len(cmds()) != 0 {
			t.Errorf("hostile container %q: expected no command, got %q", h, cmds())
		}
	}
}

func TestShellSink_ContainerLogs_ValidNamesStillRun(t *testing.T) {
	for _, n := range []string{"dozor", "go-wp", "my_container.1", "ox-codes"} {
		a, cmds := newRemoteAgent(t)
		a.GetContainerLogs(context.Background(), n, 10)
		if _, ok := fakessh.Contains(cmds(), "docker logs --tail 10 --timestamps "+n); !ok {
			t.Errorf("valid container %q must still be queried, commands: %q", n, cmds())
		}
	}
}

func TestShellSink_Metrics_HostileServiceRunsNothing(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := newRemoteAgent(t)
		a.GetMetrics(context.Background(), "today", h)
		assertNoRaw(t, cmds(), h)
	}
}

func TestShellSink_Metrics_ValidServiceStillRuns(t *testing.T) {
	a, cmds := newRemoteAgent(t)
	a.GetMetrics(context.Background(), "today", "go-wp")
	if _, ok := fakessh.Contains(cmds(), "docker inspect --format '{{.State.Pid}}' go-wp"); !ok {
		t.Errorf("valid service must still be inspected, commands: %q", cmds())
	}
}

func TestShellSink_ContainerStatusCLI_HostileNamesRunNothing(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := newRemoteAgent(t)
		a.GetServiceStatus(context.Background(), h)
		assertNoRaw(t, cmds(), h)
	}
}

func TestShellSink_ContainerStatusCLI_ValidNamesStillRun(t *testing.T) {
	for _, n := range []string{"dozor", "go-wp", "my_container.1"} {
		a, cmds := newRemoteAgent(t)
		a.GetServiceStatus(context.Background(), n)
		if _, ok := fakessh.Contains(cmds(), "--filter name="+n); !ok {
			t.Errorf("valid name %q must still be queried, commands: %q", n, cmds())
		}
	}
}

func TestShellSink_DeployHealth_HostileServicesRunNothing(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := newRemoteAgent(t)
		out := a.CheckDeployHealth(context.Background(), []string{h})
		assertNoRaw(t, cmds(), h)
		if !strings.Contains(out, "invalid services") {
			t.Errorf("hostile service %q: expected rejection, got %q", h, out)
		}
	}
}

func TestShellSink_GitPath_HostilePathsRunNothing(t *testing.T) {
	for _, h := range fakessh.HostilePaths {
		a, cmds := newRemoteAgent(t)
		a.GetGitStatusAt(context.Background(), h)
		assertNoRaw(t, cmds(), h)
		if len(cmds()) != 0 {
			t.Errorf("hostile path %q: expected no command, got %q", h, cmds())
		}
	}
}

func TestShellSink_GitPath_ValidPathStillRuns(t *testing.T) {
	a, cmds := newRemoteAgent(t)
	a.GetGitStatusAt(context.Background(), "/home/user/src/dozor")
	if _, ok := fakessh.Contains(cmds(), "git -C /home/user/src/dozor "); !ok {
		t.Errorf("valid path must still be queried, commands: %q", cmds())
	}
}

func TestShellSink_VulnScanTarget_HostileTargetsRunNothing(t *testing.T) {
	targets := []string{
		"http://x;id", "http://$(id)", "http://h/`id`", "http://a|b", "http://a&&b", "http://a\nb",
		"-h", "--help", "http://a b", `http://a"b`, "http://a'b", "http://h/../x?y=1&z", "file:///etc/passwd",
		"http://u:p@h", "http://h/?q=1", "http://h/#f", "host-without-scheme", "http://[::1]",
	}
	for _, h := range targets {
		a, cmds := newRemoteAgent(t)
		_, err := a.NewNucleiCollector(a.GetConfig()).ScanTarget(context.Background(), h)
		if err == nil || !strings.Contains(err.Error(), "invalid scan target") {
			t.Errorf("hostile target %q: expected invalid scan target error, got %v", h, err)
		}
		if len(cmds()) != 0 {
			t.Errorf("hostile target %q: expected no command, got %q", h, cmds())
		}
	}
}

func TestShellSink_VulnScanSeverities_HostileRunNothing(t *testing.T) {
	for _, sev := range []string{"high; id", "$(id)", "high,", "high low", "-h", "HIGH", "high\nlow"} {
		a, cmds := newRemoteAgent(t)
		cfg := a.GetConfig()
		cfg.NucleiSeverities = sev
		_, err := a.NewNucleiCollector(cfg).ScanTarget(context.Background(), "http://localhost:8080")
		if err == nil || !strings.Contains(err.Error(), "invalid severities") {
			t.Errorf("hostile severities %q: expected invalid severities error, got %v", sev, err)
		}
		if len(cmds()) != 0 {
			t.Errorf("hostile severities %q: expected no command, got %q", sev, cmds())
		}
	}
}

func TestShellSink_VulnScan_ValidTargetPassesValidation(t *testing.T) {
	for _, tg := range []string{"http://localhost:8080", "https://example.com/path/x", "http://10.0.0.1"} {
		a, _ := newRemoteAgent(t)
		_, err := a.NewNucleiCollector(a.GetConfig()).ScanTarget(context.Background(), tg)
		if err != nil && strings.Contains(err.Error(), "invalid scan target") {
			t.Errorf("valid target %q rejected: %v", tg, err)
		}
	}
}

func TestShellSink_LegacyDeploy_HostileInputsRunNothing(t *testing.T) {
	for _, h := range fakessh.HostilePaths {
		a, cmds := newRemoteAgent(t)
		res := a.StartDeploy(context.Background(), h, nil, true, false)
		if res.Success {
			t.Errorf("hostile project_path %q accepted", h)
		}
		assertNoRaw(t, cmds(), h)
		if len(cmds()) != 0 {
			t.Errorf("hostile project_path %q: expected no command, got %q", h, cmds())
		}
	}
	for _, h := range fakessh.HostileNames {
		a, cmds := newRemoteAgent(t)
		res := a.StartDeploy(context.Background(), "/srv/app", []string{h}, true, false)
		if res.Success {
			t.Errorf("hostile service %q accepted", h)
		}
		if len(cmds()) != 0 {
			t.Errorf("hostile service %q: expected no command, got %q", h, cmds())
		}
	}
}

func TestShellSink_LegacyDeploy_ValidInputsStillRun(t *testing.T) {
	a, cmds := newRemoteAgent(t)
	res := a.StartDeploy(context.Background(), "~/src/dozor", []string{"dozor", "go-wp"}, true, false)
	if !res.Success {
		t.Fatalf("valid deploy rejected: %s", res.Error)
	}
	if _, ok := fakessh.Contains(cmds(), "docker compose up -d --build dozor go-wp"); !ok {
		t.Errorf("valid deploy must still run, commands: %q", cmds())
	}
}
