package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCleanup(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_cleanup",
		Description: `Scan or clean system resources to free disk space.
Auto-detects: docker, go, npm, uv, pip, journal, tmp, caches.
Default: dry-run (report=true) shows reclaimable sizes.
Set report=false to execute cleanup.`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CleanupInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		// Validate targets
		if len(input.Targets) > 0 {
			if ok, reason := engine.ValidateCleanupTargets(input.Targets); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid targets: %s", reason)
			}
		}

		// Validate min_age if provided
		if input.MinAge != "" {
			if ok, reason := engine.ValidateTimeDuration(input.MinAge); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid min_age: %s", reason)
			}
		}

		// Default report=true (dry-run)
		report := true
		if input.Report != nil {
			report = *input.Report
		}

		result := agent.CleanupSystem(ctx, input.Targets, report, input.MinAge)
		return nil, engine.TextOutput{Text: result}, nil
	})
}
