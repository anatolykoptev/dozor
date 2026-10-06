package main

import (
	"log/slog"
	"os"

	"github.com/anatolykoptev/go-mcpserver"
)

// mcpTokenEnv names the pre-shared bearer token for the /mcp endpoint. The
// same variable is read by the claudecode extension so the Claude Code it
// spawns can call back into this server.
const mcpTokenEnv = "DOZOR_MCP_TOKEN"

// mcpBearerAuth returns the auth config for /mcp.
//
// /mcp serves server_exec with caller-selectable security mode, so it is a
// shell on the host for anyone who reaches the port. Reachability is not a
// boundary: the port is bound on all interfaces for docker-bridge callers,
// and on 2026-10-06 a host-netns proxy was found to hand loopback to partner
// credentials. Fail closed: an unset token rejects every /mcp request (401)
// instead of serving it, while webhooks, /metrics and /health keep working.
// DOZOR_MCP_ALLOW_INSECURE=true is the explicit dev opt-out, mirroring
// DOZOR_A2A_ALLOW_INSECURE.
func mcpBearerAuth() *mcpserver.BearerAuth {
	token := os.Getenv(mcpTokenEnv)
	if token == "" {
		if os.Getenv("DOZOR_MCP_ALLOW_INSECURE") == "true" {
			slog.Warn("MCP endpoint unauthenticated — DOZOR_MCP_ALLOW_INSECURE override active; DO NOT use in production")
			return nil
		}
		slog.Error("MCP endpoint rejecting all requests: "+mcpTokenEnv+" is not set",
			slog.String("hint", "set "+mcpTokenEnv+" and send it as 'Authorization: Bearer <token>'"))
	}
	// StaticTokenVerifier rejects every token when the expected one is empty.
	return &mcpserver.BearerAuth{Verifier: mcpserver.StaticTokenVerifier(token)}
}
