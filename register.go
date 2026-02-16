package main

import "github.com/modelcontextprotocol/go-sdk/mcp"

func registerTools(server *mcp.Server) {
	registerDiagnose(server)
	registerStatus(server)
	registerLogs(server)
	registerAnalyzeLogs(server)
	registerHealth(server)
	registerSecurity(server)
	registerExec(server)
	registerRestart(server)
	registerPrune(server)
	registerDeploy(server)
	registerDeployStatus(server)
}
