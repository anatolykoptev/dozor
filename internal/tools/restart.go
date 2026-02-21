package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerRestart(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_restart",
		Description: "Restart a docker compose service.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.RestartInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandleRestart(ctx, agent, input.Service)
		return nil, engine.TextOutput{Text: text}, err
	})
}
