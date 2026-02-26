package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerContainerExec(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_container_exec",
		Description: `Execute a shell command inside a running Docker container.
Useful for diagnostics: checking configs, running health checks, querying databases, testing connectivity.
Commands are validated against a safety blocklist (no rm -rf, no reverse shells, no fork bombs).
Container can be specified by name, compose service name, or partial match.`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ContainerExecInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if input.Container == "" {
			return nil, engine.TextOutput{}, errors.New("container name is required")
		}
		if ok, reason := engine.ValidateServiceName(input.Container); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid container name: %s", reason)
		}
		if input.Command == "" {
			return nil, engine.TextOutput{}, errors.New("command is required")
		}
		if ok, reason := engine.IsContainerExecAllowed(input.Command); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("command not allowed: %s", reason)
		}

		output, err := agent.ContainerExec(ctx, input.Container, input.Command, input.User)
		if err != nil {
			return nil, engine.TextOutput{}, err
		}

		return nil, engine.TextOutput{Text: output}, nil
	})
}
