package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// RegisterClientTools adds a2a_list_agents, a2a_discover, and a2a_call tools.
func RegisterClientTools(registry *toolreg.Registry, mgr *ClientManager) {
	registry.Register(&listAgentsTool{mgr: mgr})
	registry.Register(&discoverTool{mgr: mgr})
	registry.Register(&callTool{mgr: mgr})
}

// --- a2a_list_agents ---

type listAgentsTool struct{ mgr *ClientManager }

func (t *listAgentsTool) Name() string        { return "a2a_list_agents" }
func (t *listAgentsTool) Description() string  { return "List available remote A2A agents" }
func (t *listAgentsTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *listAgentsTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	ids := t.mgr.ListAgents()
	if len(ids) == 0 {
		return "No remote agents configured.", nil
	}
	var sb strings.Builder
	sb.WriteString("Available remote agents:\n")
	for _, id := range ids {
		agent, _ := t.mgr.GetAgent(id)
		alias := agent.Alias
		if alias == "" {
			alias = id
		}
		fmt.Fprintf(&sb, "- %s (%s) at %s\n", id, alias, agent.URL)
	}
	return sb.String(), nil
}

// --- a2a_discover ---

type discoverTool struct{ mgr *ClientManager }

func (t *discoverTool) Name() string        { return "a2a_discover" }
func (t *discoverTool) Description() string  { return "Discover capabilities of a remote A2A agent by fetching its agent card" }
func (t *discoverTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "ID of the remote agent to discover",
			},
		},
		"required": []string{"agent_id"},
	}
}

func (t *discoverTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	agentID, _ := args["agent_id"].(string)
	if agentID == "" {
		available := t.mgr.ListAgents()
		if len(available) == 0 {
			return "No remote agents configured. agent_id is required.", nil
		}
		return fmt.Sprintf("agent_id is required. Available agents: %s. Please retry with one of these IDs.", strings.Join(available, ", ")), nil
	}

	card, err := t.mgr.Discover(ctx, agentID)
	if err != nil {
		return "", err
	}

	// Pretty-print the JSON.
	var raw json.RawMessage
	if json.Unmarshal([]byte(card), &raw) == nil {
		if pretty, err := json.MarshalIndent(raw, "", "  "); err == nil {
			return string(pretty), nil
		}
	}
	return card, nil
}

// --- a2a_call ---

type callTool struct{ mgr *ClientManager }

func (t *callTool) Name() string        { return "a2a_call" }
func (t *callTool) Description() string  { return "Send a message to a remote A2A agent and get a response" }
func (t *callTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "ID of the remote agent to call",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "Text message to send to the agent",
			},
		},
		"required": []string{"agent_id", "message"},
	}
}

func (t *callTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	agentID, _ := args["agent_id"].(string)
	message, _ := args["message"].(string)
	if agentID == "" {
		available := t.mgr.ListAgents()
		if len(available) == 0 {
			return "No remote agents configured. agent_id is required.", nil
		}
		return fmt.Sprintf("agent_id is required. Available agents: %s. Please retry a2a_call with agent_id set to one of these values.", strings.Join(available, ", ")), nil
	}
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	return t.mgr.Call(ctx, agentID, message)
}
