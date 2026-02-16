package main

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerHealth(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_health",
		Description: "Quick health check of all monitored services. Returns OK/!! status for each service.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, engine.TextOutput, error) {
		return nil, engine.TextOutput{Text: agent.GetHealth(ctx)}, nil
	})
}
