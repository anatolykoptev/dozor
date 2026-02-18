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
	// New tools
	registerProbe(server, agent)
	registerCert(server, agent)
	registerPorts(server, agent)
	registerEnv(server, agent)
	registerGit(server, agent)
	// Web tools moved to extension system (pkg/extensions/websearch/)
}
