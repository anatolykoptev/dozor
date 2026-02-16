package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerInspect(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_inspect",
		Description: `Inspect server state. Modes:
- health: quick OK/!! status of all services
- status: detailed status for one service (CPU, memory, uptime, errors)
- diagnose: full diagnostics with alerts and health assessment
- logs: recent logs for a service (supports line count)
- analyze: error pattern analysis with remediation suggestions
- security: security audit (network, containers, auth, API hardening)
- overview: system dashboard (disk, memory, load, top processes, docker summary)
- remote: remote server monitoring (HTTP, SSL, systemd services via SSH)
- systemd: local systemd service monitoring`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.InspectInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		switch input.Mode {
		case "health":
			return nil, engine.TextOutput{Text: agent.GetHealth(ctx)}, nil

		case "status":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service is required for status mode")
			}
			if ok, reason := engine.ValidateServiceName(input.Service); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
			}
			s := agent.GetServiceStatus(ctx, input.Service)
			return nil, engine.TextOutput{Text: engine.FormatStatus(s)}, nil

		case "diagnose":
			report := agent.Diagnose(ctx, input.Services)
			text := engine.FormatReport(report)
			if report.NeedsAttention() {
				data, _ := json.MarshalIndent(report, "", "  ")
				text += "\n\n<diagnostic_data>\n" + string(data) + "\n</diagnostic_data>"
			}
			return nil, engine.TextOutput{Text: text}, nil

		case "logs":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service is required for logs mode")
			}
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
			entries := agent.GetLogs(ctx, input.Service, lines, false)
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

		case "analyze":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service is required for analyze mode")
			}
			if ok, reason := engine.ValidateServiceName(input.Service); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
			}
			result := agent.AnalyzeLogs(ctx, input.Service)
			return nil, engine.TextOutput{Text: engine.FormatAnalysis(result)}, nil

		case "security":
			issues := agent.CheckSecurity(ctx)
			return nil, engine.TextOutput{Text: engine.FormatSecurityReport(issues)}, nil

		case "overview":
			return nil, engine.TextOutput{Text: agent.GetOverview(ctx)}, nil

		case "remote":
			return nil, engine.TextOutput{Text: agent.GetRemoteStatus(ctx)}, nil

		case "systemd":
			return nil, engine.TextOutput{Text: agent.GetSystemdStatus(ctx, input.Services)}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown mode %q, use: health, status, diagnose, logs, analyze, security, overview, remote, systemd", input.Mode)
		}
	})
}
