package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerInspect(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_inspect",
		Description: `Inspect server state. Modes:
- health: quick OK/!! status of all services
- status: detailed status for one service (CPU, memory, uptime, errors) [requires: service]
- diagnose: full diagnostics with alerts and health assessment
- logs: recent logs for a service (supports line count) [requires: service]
- analyze: error pattern analysis with remediation suggestions
- errors: ERROR/FATAL log lines from all services in one call
- security: security audit (network, containers, auth, API hardening)
- overview: system dashboard (disk, memory, load, top processes, docker summary)
- remote: remote server monitoring (HTTP, SSL, systemd services via SSH)
- systemd: local systemd service monitoring
- connections: TCP/UDP connections by state, top remote IPs, per-service counts
- cron: all cron jobs, systemd timers, and at jobs`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.InspectInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandleInspect(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
