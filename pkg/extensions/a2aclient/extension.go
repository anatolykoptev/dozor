package a2aclient

import (
	"context"
	"os"

	"github.com/anatolykoptev/dozor/internal/a2a"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/pkg/extensions"
)

// A2AClientExtension provides remote A2A agent connectivity.
type A2AClientExtension struct {
	manager *a2a.ClientManager
}

func New() *A2AClientExtension { return &A2AClientExtension{} }

func (e *A2AClientExtension) Name() string { return "a2aclient" }

func (e *A2AClientExtension) GetCapabilities() extensions.Capabilities {
	return extensions.Capabilities{
		Tools:     true,
		MCPTools:  false,
		Config:    true,
		Lifecycle: false,
	}
}

func (e *A2AClientExtension) ValidateConfig(_ *engine.Config) extensions.ConfigValidation {
	agentsEnv := os.Getenv("DOZOR_A2A_AGENTS")
	if agentsEnv == "" {
		// Not an error — A2A is optional
		return extensions.ConfigValidation{OK: false, Errors: []extensions.ConfigError{{
			Field:   "DOZOR_A2A_AGENTS",
			Message: "no remote A2A agents configured, extension will be skipped",
		}}}
	}
	return extensions.ConfigValidation{OK: true}
}

func (e *A2AClientExtension) GetConfigHints() map[string]extensions.ConfigHint {
	return map[string]extensions.ConfigHint{
		"DOZOR_A2A_AGENTS": {
			Label:       "Remote A2A Agents",
			Help:        "Comma-separated list of id=url pairs",
			Placeholder: "orchestrator=http://127.0.0.1:18790,devops=http://127.0.0.1:18793",
		},
		"DOZOR_A2A_SECRET": {
			Label:     "A2A Bearer Secret",
			Help:      "Token for authenticating A2A endpoint",
			Sensitive: true,
		},
	}
}

func (e *A2AClientExtension) Register(ctx context.Context, extCtx *extensions.Context) error {
	agentsEnv := os.Getenv("DOZOR_A2A_AGENTS")
	if agentsEnv == "" {
		extCtx.Runtime.Logger.Info("no A2A agents configured, skipping")
		return nil
	}

	remoteAgents := a2a.ParseAgentsEnv(agentsEnv)
	if len(remoteAgents) == 0 {
		extCtx.Runtime.Logger.Warn("DOZOR_A2A_AGENTS set but parsed no valid agents")
		return nil
	}

	e.manager = a2a.NewClientManager(remoteAgents)

	if extCtx.Tools != nil {
		a2a.RegisterClientTools(extCtx.Tools, e.manager)
	}

	extCtx.Runtime.Logger.Info("a2a client registered", "agents", len(remoteAgents))
	return nil
}
