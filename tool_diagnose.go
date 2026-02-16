package main

import (
	"context"
	"encoding/json"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerDiagnose(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_diagnose",
		Description: "Run full server diagnostics. Returns service statuses, resource usage, alerts, and overall health assessment.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.DiagnoseInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		report := agent.Diagnose(ctx, input.Services)
		text := engine.FormatReport(report)

		if report.NeedsAttention() {
			data, _ := json.MarshalIndent(report, "", "  ")
			text += "\n\n<diagnostic_data>\n" + string(data) + "\n</diagnostic_data>"
		}

		return nil, engine.TextOutput{Text: text}, nil
	})
}
