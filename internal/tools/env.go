package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerEnv(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_env",
		Description: `Inspect environment variables of a running Docker Compose service container.
Validates required variables, detects empty secrets, and flags default/placeholder values.
Sensitive fields (_KEY, _SECRET, _TOKEN, _PASSWORD) are redacted in output.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.EnvInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if input.Service == "" {
			return nil, engine.TextOutput{}, errors.New("service name is required")
		}
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid service: %s", reason)
		}
		result := agent.CheckContainerEnv(ctx, input.Service, input.Required)
		return nil, engine.TextOutput{Text: result}, nil
	})
}
