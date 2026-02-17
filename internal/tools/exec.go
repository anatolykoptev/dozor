package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerExec(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_exec",
		Description: "Execute a validated shell command on the server. Commands are checked against a blocklist for safety.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ExecInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if input.Command == "" {
			return nil, engine.TextOutput{}, fmt.Errorf("command is required")
		}
		if ok, reason := engine.IsCommandAllowed(input.Command); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("command blocked: %s", reason)
		}

		result := agent.ExecuteCommand(ctx, input.Command)
		if !result.Success {
			return nil, engine.TextOutput{Text: fmt.Sprintf("Command failed (exit %d):\n%s", result.ReturnCode, result.Output())}, nil
		}
		return nil, engine.TextOutput{Text: result.Output()}, nil
	})
}
