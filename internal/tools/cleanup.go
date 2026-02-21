package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCleanup(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_cleanup",
		Description: `Scan or clean system resources to free disk space.
Auto-detects: docker, go, npm, uv, pip, journal, tmp, caches, memory.
memory target: kills stale orphaned processes (claude/gopls with dead parents) and flushes swap.
Default: dry-run (report=true) shows reclaimable sizes.
Set report=false to execute cleanup.`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CleanupInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandleCleanup(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
