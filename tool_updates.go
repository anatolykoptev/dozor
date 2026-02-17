package main

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerUpdates(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_updates",
		Description: `Check and install updates for CLI binaries installed from GitHub releases.
- check: scan configured + auto-detected binaries in ~/.local/bin/, compare with latest GitHub release
- install: download latest release and replace binary (requires binary name)
Auto-detects ~60 popular CLIs (gh, lazygit, fzf, bat, fd, delta, etc.).
Configure DOZOR_TRACKED_BINARIES for custom binaries.
Set DOZOR_GITHUB_TOKEN for higher API rate limits (60/hr -> 5000/hr).`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.UpdatesInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		switch input.Action {
		case "check":
			binaries := agent.CheckUpdates(ctx)
			return nil, engine.TextOutput{Text: engine.FormatUpdatesCheck(binaries)}, nil

		case "install":
			if input.Binary == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("binary name is required for install action")
			}
			if ok, reason := engine.ValidateBinaryName(input.Binary); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid binary name: %s", reason)
			}
			result, err := agent.InstallUpdate(ctx, input.Binary)
			if err != nil {
				return nil, engine.TextOutput{}, err
			}
			return nil, engine.TextOutput{Text: result}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown action %q, use: check, install", input.Action)
		}
	})
}
