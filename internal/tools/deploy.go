package tools

import (
	"context"

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
		text, err := HandleDeploy(ctx, agent, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}
