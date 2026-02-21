package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerUpdates(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_updates",
		Description: `Check and install updates for CLI binaries installed from GitHub releases.
- check: scan configured + auto-detected binaries in ~/.local/bin/, compare with latest GitHub release
- install: download latest release and replace binary (requires binary name)
Auto-detects ~60 popular CLIs (gh, lazygit, fzf, bat, fd, delta, etc.).
Configure DOZOR_TRACKED_BINARIES for custom binaries.
Set DOZOR_GITHUB_TOKEN for higher API rate limits (60/hr -> 5000/hr).`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.UpdatesInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := HandleUpdates(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
