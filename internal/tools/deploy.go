package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerDeploy(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_deploy",
		Description: `Deploy or check deploy status. Actions:
- deploy (default): start a new background deployment (pulls, builds, docker compose up)
- status: check status of an existing deploy by deploy_id
- health: wait 10s then verify all services are running (post-deploy check)`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.DeployInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		// Explicit health check action
		if input.Action == "health" {
			result := agent.CheckDeployHealth(ctx, input.Services)
			return nil, engine.TextOutput{Text: result}, nil
		}

		// Check status mode (legacy: deploy_id provided, or action=status)
		if input.DeployID != "" || input.Action == "status" {
			if input.DeployID == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("deploy_id is required for status action")
			}
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

		return nil, engine.TextOutput{Text: fmt.Sprintf(
			"Deploy started.\nID: %s\nLog: %s\n\nCheck status: server_deploy({deploy_id: %q})\nVerify health: server_deploy({action: \"health\"})",
			result.DeployID, result.LogFile, result.DeployID)}, nil
	})
}
