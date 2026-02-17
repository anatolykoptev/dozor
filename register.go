package main

import "github.com/modelcontextprotocol/go-sdk/mcp"

func registerTools(server *mcp.Server) {
	registerInspect(server)
	registerTriage(server)
	registerExec(server)
	registerRestart(server)
	registerDeploy(server)
	registerPrune(server)
	registerCleanup(server)
	registerServices(server)
	registerUpdates(server)
}
