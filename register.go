package main

import "github.com/modelcontextprotocol/go-sdk/mcp"

func registerTools(server *mcp.Server) {
	registerInspect(server)
	registerExec(server)
	registerRestart(server)
	registerDeploy(server)
	registerPrune(server)
}
