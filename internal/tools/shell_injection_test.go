package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/testutil/fakessh"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newRecordingAgent(t *testing.T) (*engine.ServerAgent, func() []string) {
	t.Helper()
	cmds := fakessh.Install(t)
	a := engine.NewAgent(engine.Config{Host: "fake.invalid", SSHPort: 22, Timeout: 5 * time.Second, LogLines: 10, ComposePath: "/srv/app"})
	t.Cleanup(a.Close)
	return a, cmds
}

func assertNoHostile(t *testing.T, cmds []string, raw string) {
	t.Helper()
	if c, bad := fakessh.Contains(cmds, raw); bad {
		t.Errorf("hostile input %q reached a shell command: %q", raw, c)
	}
}

func TestHandleInspect_HostileNamesRejected(t *testing.T) {
	for _, mode := range []string{"systemd", "diagnose", "container_logs", "metrics", "status", "logs", "analyze"} {
		for _, h := range fakessh.HostileNames {
			a, cmds := newRecordingAgent(t)
			in := engine.InspectInput{Mode: mode, Service: h, Services: []string{h}, Lines: 10}
			if _, err := HandleInspect(context.Background(), a, in); err == nil {
				t.Errorf("mode=%s hostile %q: expected an error", mode, h)
			}
			assertNoHostile(t, cmds(), h)
			if len(cmds()) != 0 {
				t.Errorf("mode=%s hostile %q: expected no command, got %q", mode, h, cmds())
			}
		}
	}
}

func TestHandleInspect_ValidNamesStillWork(t *testing.T) {
	cases := map[string][]string{
		"systemd":        {"dozor", "user@1000.service", "nginx.service"},
		"container_logs": {"go-wp", "my_container.1"},
	}
	for mode, names := range cases {
		for _, n := range names {
			a, cmds := newRecordingAgent(t)
			in := engine.InspectInput{Mode: mode, Service: n, Services: []string{n}, Lines: 10}
			if _, err := HandleInspect(context.Background(), a, in); err != nil {
				t.Errorf("mode=%s valid %q rejected: %v", mode, n, err)
			}
			if _, ok := fakessh.Contains(cmds(), n); !ok {
				t.Errorf("mode=%s valid %q: command never ran, commands: %q", mode, n, cmds())
			}
		}
	}
}

func TestHandleDeploy_HostileInputsRejected(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		for _, in := range []engine.DeployInput{
			{Action: "health", Services: []string{h}},
			{Services: []string{h}, ProjectPath: "/srv/app"},
		} {
			a, cmds := newRecordingAgent(t)
			if _, err := HandleDeploy(context.Background(), a, in); err == nil {
				t.Errorf("hostile service %q: expected an error (%+v)", h, in)
			}
			assertNoHostile(t, cmds(), h)
		}
	}
	for _, h := range fakessh.HostilePaths {
		a, cmds := newRecordingAgent(t)
		if _, err := HandleDeploy(context.Background(), a, engine.DeployInput{ProjectPath: h}); err == nil {
			t.Errorf("hostile project_path %q: expected an error", h)
		}
		assertNoHostile(t, cmds(), h)
	}
}

// callTool drives a registered MCP tool through an in-memory client session,
// the same path an MCP caller takes.
func callTool(t *testing.T, a *engine.ServerAgent, register func(*mcp.Server, *engine.ServerAgent), name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	register(srv, a)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMCPTool_ServerInspect_SystemdHostileRejected(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := newRecordingAgent(t)
		res := callTool(t, a, registerInspect, "server_inspect", map[string]any{"mode": "systemd", "services": []string{h}})
		if !res.IsError {
			t.Errorf("hostile %q: expected a tool error", h)
		}
		assertNoHostile(t, cmds(), h)
	}
}

func TestMCPTool_ServerGit_HostilePathRejected(t *testing.T) {
	for _, h := range fakessh.HostilePaths {
		a, cmds := newRecordingAgent(t)
		callTool(t, a, registerGit, "server_git", map[string]any{"path": h})
		assertNoHostile(t, cmds(), h)
		if len(cmds()) != 0 {
			t.Errorf("hostile path %q: expected no command, got %q", h, cmds())
		}
	}
}

func TestMCPTool_ServerGit_ValidPathRuns(t *testing.T) {
	a, cmds := newRecordingAgent(t)
	callTool(t, a, registerGit, "server_git", map[string]any{"path": "/home/user/src/dozor"})
	if _, ok := fakessh.Contains(cmds(), "git -C /home/user/src/dozor "); !ok {
		t.Errorf("valid path must still run, commands: %q", cmds())
	}
}

func TestMCPTool_ServerVulnScan_HostileInputsRejected(t *testing.T) {
	targets := []string{"http://x;id", "http://$(id)", "http://h/`id`", "http://a|b", "http://a&&b", "http://a\nb", "-h", "--help", "http://a b", "http://h/?a=1&b=2", "file:///etc/passwd"}
	for _, h := range targets {
		a, cmds := newRecordingAgent(t)
		res := callTool(t, a, registerNuclei, "server_vuln_scan", map[string]any{"target": h})
		if !res.IsError {
			t.Errorf("hostile target %q: expected a tool error", h)
		}
		assertNoHostile(t, cmds(), h)
		if len(cmds()) != 0 {
			t.Errorf("hostile target %q: expected no command, got %q", h, cmds())
		}
	}
	for _, sev := range []string{"high; id", "$(id)", "-h", "bogus"} {
		a, cmds := newRecordingAgent(t)
		res := callTool(t, a, registerNuclei, "server_vuln_scan", map[string]any{"target": "http://localhost:8080", "severities": sev})
		if !res.IsError {
			t.Errorf("hostile severities %q: expected a tool error", sev)
		}
		assertNoHostile(t, cmds(), sev)
	}
}

func TestHandleDeploy_HomeRelativeProjectPathStillPasses(t *testing.T) {
	a, cmds := newRecordingAgent(t)
	if _, err := HandleDeploy(context.Background(), a, engine.DeployInput{ProjectPath: "~/src/x", Services: []string{"x"}}); err != nil {
		t.Fatalf("valid ~/ project_path rejected: %v", err)
	}
	if _, ok := fakessh.Contains(cmds(), "cd $HOME/src/x"); !ok {
		t.Errorf("valid ~/ deploy must still run, commands: %q", cmds())
	}
}

func TestMCPTool_ServerGit_HomeRelativePathStillRuns(t *testing.T) {
	a, cmds := newRecordingAgent(t)
	callTool(t, a, registerGit, "server_git", map[string]any{"path": "~/src/dozor"})
	if _, ok := fakessh.Contains(cmds(), "git -C ~/src/dozor "); !ok {
		t.Errorf("~/ path must still run, commands: %q", cmds())
	}
	for _, rel := range []string{".", "src/dozor", "~"} {
		a2, cmds2 := newRecordingAgent(t)
		res := callTool(t, a2, registerGit, "server_git", map[string]any{"path": rel})
		if !res.IsError || len(cmds2()) != 0 {
			t.Errorf("relative path %q must be rejected with no command (isError=%v, cmds=%q)", rel, res.IsError, cmds2())
		}
	}
}

func TestFormatNucleiResults_ReportsSkippedTargets(t *testing.T) {
	out := formatNucleiResults(nil, []string{"http://x;id: invalid scan target"})
	if out == "No vulnerabilities found." || !strings.Contains(out, "NOT scanned") {
		t.Errorf("skipped targets must be reported, got %q", out)
	}
	if got := formatNucleiResults(nil, nil); got != "No vulnerabilities found." {
		t.Errorf("clean result changed: %q", got)
	}
}
