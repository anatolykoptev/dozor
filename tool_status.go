package main

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerStatus(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_status",
		Description: "Get detailed status for a specific service including state, health, uptime, CPU, memory, and error count.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.StatusInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
		}
		s := agent.GetServiceStatus(ctx, input.Service)
		return nil, engine.TextOutput{Text: engine.FormatStatus(s)}, nil
	})
}
