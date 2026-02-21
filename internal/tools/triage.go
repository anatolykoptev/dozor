package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerTriage(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_triage",
		Description: `Full auto-diagnosis in one call. Discovers all services, checks health, and for problematic services automatically analyzes error patterns, shows recent errors, and suggests remediation. Includes disk pressure alerts.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.TriageInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandleTriage(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
