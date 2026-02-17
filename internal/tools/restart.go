package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerRestart(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_restart",
		Description: "Restart a docker compose service.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.RestartInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
		}
		result := agent.RestartService(ctx, input.Service)
		if !result.Success {
			return nil, engine.TextOutput{Text: fmt.Sprintf("Restart failed: %s", result.Output())}, nil
		}
		return nil, engine.TextOutput{Text: fmt.Sprintf("Service %s restarted successfully.", input.Service)}, nil
	})
}
