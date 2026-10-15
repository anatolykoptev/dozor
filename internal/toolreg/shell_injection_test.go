package toolreg

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/testutil/fakessh"
)

func recordingAgent(t *testing.T) (*engine.ServerAgent, func() []string) {
	t.Helper()
	cmds := fakessh.Install(t)
	a := engine.NewAgent(engine.Config{Host: "fake.invalid", SSHPort: 22, Timeout: 5 * time.Second, LogLines: 10})
	t.Cleanup(a.Close)
	return a, cmds
}

// The LLM-loop bridge forwards model-chosen arguments unchanged, so it must
// be covered by the same validation as the MCP handlers.
func TestBridge_InspectSystemd_HostileRejected(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := recordingAgent(t)
		tool := &inspectTool{agent: a}
		_, err := tool.Execute(context.Background(), map[string]any{"mode": "systemd", "services": []any{h}})
		if err == nil {
			t.Errorf("hostile %q: expected an error", h)
		}
		if c, bad := fakessh.Contains(cmds(), h); bad {
			t.Errorf("hostile %q reached a shell command: %q", h, c)
		}
	}
}

func TestBridge_InspectContainerLogs_HostileRejected(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := recordingAgent(t)
		tool := &inspectTool{agent: a}
		_, err := tool.Execute(context.Background(), map[string]any{"mode": "container_logs", "service": h})
		if err == nil {
			t.Errorf("hostile %q: expected an error", h)
		}
		if len(cmds()) != 0 {
			t.Errorf("hostile %q: expected no command, got %q", h, cmds())
		}
	}
}

func TestBridge_DeployHealth_HostileRejected(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		a, cmds := recordingAgent(t)
		tool := &deployTool{agent: a}
		_, err := tool.Execute(context.Background(), map[string]any{"action": "health", "services": []any{h}})
		if err == nil {
			t.Errorf("hostile %q: expected an error", h)
		}
		if c, bad := fakessh.Contains(cmds(), h); bad {
			t.Errorf("hostile %q reached a shell command: %q", h, c)
		}
	}
}

func TestBridge_InspectSystemd_ValidStillRuns(t *testing.T) {
	a, cmds := recordingAgent(t)
	tool := &inspectTool{agent: a}
	if _, err := tool.Execute(context.Background(), map[string]any{"mode": "systemd", "services": []any{"user@1000.service"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fakessh.Contains(cmds(), "systemctl is-active user@1000.service"); !ok {
		t.Errorf("valid unit must still run, commands: %q", cmds())
	}
}
