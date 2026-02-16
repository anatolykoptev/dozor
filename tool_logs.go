package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerLogs(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_logs",
		Description: "Get recent logs for a service. Supports filtering to errors-only and configurable line count.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.LogsInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
		}
		lines := input.Lines
		if lines <= 0 {
			lines = 100
		}
		if lines > 10000 {
			return nil, engine.TextOutput{}, fmt.Errorf("lines must be <= 10000")
		}

		entries := agent.GetLogs(ctx, input.Service, lines, input.ErrorsOnly)

		// Limit output to last 50 entries
		if len(entries) > 50 {
			entries = entries[len(entries)-50:]
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Logs for %s (%d entries):\n\n", input.Service, len(entries))
		for _, e := range entries {
			if e.Timestamp != nil {
				fmt.Fprintf(&b, "[%s] ", e.Timestamp.Format("15:04:05"))
			}
			fmt.Fprintf(&b, "[%s] %s\n", e.Level, e.Message)
		}

		return nil, engine.TextOutput{Text: b.String()}, nil
	})
}

func registerAnalyzeLogs(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_analyze_logs",
		Description: "Analyze recent logs for a service. Detects error patterns, categorizes issues, and suggests remediation.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.AnalyzeLogsInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
		}
		result := agent.AnalyzeLogs(ctx, input.Service)
		return nil, engine.TextOutput{Text: engine.FormatAnalysis(result)}, nil
	})
}
