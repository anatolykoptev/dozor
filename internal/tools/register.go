package tools

import (
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAll registers all MCP tools using default exec options (mode from env/defaults).
func RegisterAll(server *mcp.Server, agent *engine.ServerAgent) {
	RegisterAllWithOpts(server, agent, ExecOptions{Config: NewExecConfig()})
}

// RegisterAllWithOpts registers all MCP tools, forwarding ExecOptions to the exec tool.
// If execOpts.Config is nil, a new ExecConfig is created from env.
func RegisterAllWithOpts(server *mcp.Server, agent *engine.ServerAgent, execOpts ExecOptions) {
	if execOpts.Config == nil {
		execOpts.Config = NewExecConfig()
	}
	registerInspect(server, agent)
	registerTriage(server, agent)
	registerExec(server, agent, execOpts)
	registerExecSecurity(server, execOpts.Config)
	registerRemoteExec(server, agent)
	registerRestart(server, agent)
	registerDeploy(server, agent)
	registerPrune(server, agent)
	registerCleanup(server, agent)
	registerServices(server, agent)
	registerUpdates(server, agent)
	registerRemote(server, agent)
	// New tools
	registerContainerExec(server, agent)
	registerProbe(server, agent)
	registerCert(server, agent)
	registerPorts(server, agent)
	registerEnv(server, agent)
	registerGit(server, agent)
	// Web tools moved to extension system (pkg/extensions/websearch/)
}
