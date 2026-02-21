package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerRemoteExec(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_remote_exec",
		Description: "Execute a validated shell command on the remote server via SSH. Commands are checked against a blocklist for safety. Requires DOZOR_REMOTE_HOST to be configured.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.RemoteExecInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandleRemoteExec(ctx, agent, input.Command)
		return nil, engine.TextOutput{Text: text}, err
	})
}
