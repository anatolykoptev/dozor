package tools

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerPorts(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_ports",
		Description: `Audit all listening network ports on the server.
Shows port, protocol (TCP/UDP), bind address, and process name.
Highlights ports exposed to 0.0.0.0 (all interfaces) vs internal-only (127.0.0.1).
Use to detect unintended public exposure of services like databases or internal APIs.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ engine.PortsInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		ports := agent.ScanPorts(ctx)
		return nil, engine.TextOutput{Text: engine.FormatPorts(ports)}, nil
	})
}
