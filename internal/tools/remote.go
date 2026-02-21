package tools

import (
	"context"

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
		text, err := HandleRemote(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
