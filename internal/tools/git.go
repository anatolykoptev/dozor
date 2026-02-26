package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerGit(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_git",
		Description: `Show git deployment status for a repository on the server.
Displays: current branch, last commit (hash, message, author, date), dirty working tree, and sync with origin.
Defaults to the directory containing docker-compose.yml if no path is given.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.GitInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if input.Path != "" {
			if strings.Contains(input.Path, "..") {
				return nil, engine.TextOutput{}, errors.New("path must not contain '..'")
			}
		}
		status := agent.GetGitStatusAt(ctx, input.Path)
		return nil, engine.TextOutput{Text: engine.FormatGitStatus(status)}, nil
	})
}
