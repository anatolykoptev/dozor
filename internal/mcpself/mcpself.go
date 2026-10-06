// Package mcpself builds the MCP client entry dozor hands to the Claude Code
// processes it spawns so they can call back into dozor's own /mcp. There is
// one builder because /mcp requires a bearer token: a copy that forgets the
// header gets 401, and Claude Code silently drops a server that fails, so
// the session runs without dozor's tools and nothing reports it.
package mcpself

import "os"

// TokenEnv names the pre-shared bearer token required by dozor's /mcp.
const TokenEnv = "DOZOR_MCP_TOKEN"

// ServerEntry returns the mcpServers entry for dozor at url, carrying the
// bearer token when one is configured. Callers write it to a 0600 file
// (os.CreateTemp) and must remove that file when the process exits.
func ServerEntry(url string) map[string]any {
	entry := map[string]any{
		"type": "http",
		"url":  url,
	}
	if token := os.Getenv(TokenEnv); token != "" {
		entry["headers"] = map[string]string{"Authorization": "Bearer " + token}
	}
	return entry
}
