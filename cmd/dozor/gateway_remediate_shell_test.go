package main

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/testutil/fakessh"
)

// A triage report is free text that can carry attacker-influenced content, and
// tryAutoRemediate restarts whatever service name a line parses to.
func TestAutoRemediate_ForgedTriageServiceRunsNothing(t *testing.T) {
	for _, h := range fakessh.HostileNames {
		cmds := fakessh.Install(t)
		eng := engine.NewAgent(engine.Config{Host: "fake.invalid", SSHPort: 22, Timeout: 5 * time.Second, ComposePath: "/srv/app"})
		report := "[CRITICAL] " + h + engine.TriageMachineSep + "container down\n"
		tryAutoRemediate(context.Background(), eng, engine.Config{}, report, nil)
		eng.Close()
		if c, bad := fakessh.Contains(cmds(), h); bad {
			t.Errorf("forged triage service %q reached a shell command: %q", h, c)
		}
	}
}

func TestAutoRemediate_ValidServiceStillRestarts(t *testing.T) {
	cmds := fakessh.Install(t)
	eng := engine.NewAgent(engine.Config{Host: "fake.invalid", SSHPort: 22, Timeout: 5 * time.Second, ComposePath: "/srv/app"})
	defer eng.Close()
	tryAutoRemediate(context.Background(), eng, engine.Config{}, "[CRITICAL] go-wp"+engine.TriageMachineSep+"container down\n", nil)
	if _, ok := fakessh.Contains(cmds(), "docker compose restart go-wp"); !ok {
		t.Errorf("valid service must still be restarted, commands: %q", cmds())
	}
}
