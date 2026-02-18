package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// RegisterTools adds mcp_list_servers, mcp_discover, and mcp_call tools.
func RegisterTools(registry *toolreg.Registry, mgr *ClientManager) {
	registry.Register(&listServersTool{mgr: mgr})
	registry.Register(&discoverTool{mgr: mgr})
	registry.Register(&callTool{mgr: mgr})
}

// --- mcp_list_servers ---

type listServersTool struct{ mgr *ClientManager }

func (t *listServersTool) Name() string        { return "mcp_list_servers" }
func (t *listServersTool) Description() string { return "List configured remote MCP servers" }
func (t *listServersTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
func (t *listServersTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	ids := t.mgr.ListServers()
	if len(ids) == 0 {
		return "No remote MCP servers configured.", nil
	}
	var sb strings.Builder
	sb.WriteString("Configured remote MCP servers:\n")
	for _, id := range ids {
		server, _ := t.mgr.GetServer(id)
		alias := server.Alias
		if alias == "" {
			alias = id
		}
		fmt.Fprintf(&sb, "- %s (%s) at %s\n", id, alias, server.URL)
	}
	return sb.String(), nil
}

// --- mcp_discover ---

type discoverTool struct{ mgr *ClientManager }

func (t *discoverTool) Name() string { return "mcp_discover" }
func (t *discoverTool) Description() string {
	return "Discover tools available on a remote MCP server"
}
func (t *discoverTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"server_id": map[string]any{
				"type":        "string",
				"description": "ID of the remote MCP server to discover",
			},
		},
		"required": []string{"server_id"},
	}
}
func (t *discoverTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	serverID, _ := args["server_id"].(string)
	if serverID == "" {
		return "", fmt.Errorf("server_id is required")
	}

	tools, err := t.mgr.Discover(ctx, serverID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Tools on %s (%d):\n\n", serverID, len(tools))
	for _, t := range tools {
		fmt.Fprintf(&sb, "- **%s**: %s\n", t.Name, t.Description)
	}
	return sb.String(), nil
}

// --- mcp_call ---

type callTool struct{ mgr *ClientManager }

func (t *callTool) Name() string { return "mcp_call" }
func (t *callTool) Description() string {
	return "Call a tool on a remote MCP server. Pass tool name and parameters as JSON."
}
func (t *callTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"server_id": map[string]any{
				"type":        "string",
				"description": "ID of the remote MCP server",
			},
			"tool_name": map[string]any{
				"type":        "string",
				"description": "Name of the tool to call",
			},
			"params": map[string]any{
				"type":        "object",
				"description": "Tool parameters as JSON object",
			},
		},
		"required": []string{"server_id", "tool_name"},
	}
}
func (t *callTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	serverID, _ := args["server_id"].(string)
	toolName, _ := args["tool_name"].(string)

	if serverID == "" {
		return "", fmt.Errorf("server_id is required")
	}
	if toolName == "" {
		return "", fmt.Errorf("tool_name is required")
	}

	// Extract params (may be nil or map)
	var params map[string]any
	if p, ok := args["params"]; ok {
		if m, ok := p.(map[string]any); ok {
			params = m
		}
	}

	result, err := t.mgr.Call(ctx, serverID, toolName, params)
	if err != nil {
		return "", err
	}

	// Try to pretty-print JSON
	var raw json.RawMessage
	if json.Unmarshal([]byte(result), &raw) == nil {
		if pretty, err := json.MarshalIndent(raw, "", "  "); err == nil {
			return string(pretty), nil
		}
	}
	return result, nil
}
