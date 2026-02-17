package tools

import (
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAll registers all MCP tools on the server, injecting the agent dependency.
func RegisterAll(server *mcp.Server, agent *engine.ServerAgent) {
	registerInspect(server, agent)
	registerTriage(server, agent)
	registerExec(server, agent)
	registerRemoteExec(server, agent)
	registerRestart(server, agent)
	registerDeploy(server, agent)
	registerPrune(server, agent)
	registerCleanup(server, agent)
	registerServices(server, agent)
	registerUpdates(server, agent)
	registerRemote(server, agent)
}
