package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerRemote(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_remote",
		Description: `Manage remote server services (system-level systemd via sudo). Actions:
- status: show all remote services (active/inactive, uptime, memory)
- restart: restart a specific remote service
- logs: show recent journalctl logs for a remote service`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.RemoteInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		cfg := agent.GetConfig()
		if !cfg.HasRemote() || len(cfg.RemoteServices) == 0 {
			return nil, engine.TextOutput{}, fmt.Errorf("no remote services configured (set DOZOR_REMOTE_HOST and DOZOR_REMOTE_SERVICES in .env)")
		}

		switch input.Action {
		case "status":
			return nil, engine.TextOutput{Text: engine.RemoteServiceStatus(ctx, cfg)}, nil

		case "restart":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service name is required for restart action")
			}
			if !engine.IsValidRemoteService(cfg, input.Service) {
				return nil, engine.TextOutput{}, fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
			}
			return nil, engine.TextOutput{Text: engine.RemoteRestart(ctx, cfg, input.Service)}, nil

		case "logs":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service name is required for logs action")
			}
			if !engine.IsValidRemoteService(cfg, input.Service) {
				return nil, engine.TextOutput{}, fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
			}
			lines := input.Lines
			if lines <= 0 {
				lines = 50
			}
			if lines > 5000 {
				lines = 5000
			}
			return nil, engine.TextOutput{Text: engine.RemoteLogs(ctx, cfg, input.Service, lines)}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown action %q, use: status, restart, logs", input.Action)
		}
	})
}
