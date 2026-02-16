package main

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSecurity(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_security",
		Description: "Run security audit. Checks network exposure, container security, authentication, API hardening, and reconnaissance activity.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, engine.TextOutput, error) {
		issues := agent.CheckSecurity(ctx)
		return nil, engine.TextOutput{Text: engine.FormatSecurityReport(issues)}, nil
	})
}
