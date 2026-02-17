package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerDeploy(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_deploy",
		Description: "Deploy or check deploy status. If deploy_id is provided, checks existing deploy status. Otherwise starts a new background deployment (pulls, builds, docker compose up).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.DeployInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		// Check status mode
		if input.DeployID != "" {
			if ok, reason := engine.ValidateDeployID(input.DeployID); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid deploy ID: %s", reason)
			}
			status := agent.GetDeployStatus(ctx, input.DeployID)
			text := fmt.Sprintf("Deploy: %s\nStatus: %s\nProcess running: %v\nLog file: %s\n",
				input.DeployID, status.Status, status.ProcessRunning, status.LogFile)
			if status.LogContent != "" {
				lines := status.LogContent
				if len(lines) > 3000 {
					lines = "...\n" + lines[len(lines)-3000:]
				}
				text += "\nLog output:\n" + lines
			}
			return nil, engine.TextOutput{Text: text}, nil
		}

		// Start new deploy
		build := true
		if input.Build != nil {
			build = *input.Build
		}
		pull := true
		if input.Pull != nil {
			pull = *input.Pull
		}

		result := agent.StartDeploy(ctx, input.ProjectPath, input.Services, build, pull)
		if !result.Success {
			return nil, engine.TextOutput{}, fmt.Errorf("deploy failed: %s", result.Error)
		}

		return nil, engine.TextOutput{Text: fmt.Sprintf("Deploy started.\nID: %s\nLog: %s\n\nCheck status with server_deploy({deploy_id: %q}).",
			result.DeployID, result.LogFile, result.DeployID)}, nil
	})
}
