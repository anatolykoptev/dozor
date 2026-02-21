package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerPrune(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_prune",
		Description: "Clean up Docker resources (unused images, build cache, volumes). Shows disk usage after cleanup.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.PruneInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandlePrune(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
