package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// RemoteAgent describes a remote A2A agent.
type RemoteAgent struct {
	URL   string
	Token string
	Alias string
}

// ClientManager manages connections to remote A2A agents.
type ClientManager struct {
	agents map[string]RemoteAgent
	cards  map[string]json.RawMessage // cached agent cards
	mu     sync.RWMutex
	client *http.Client
}

// NewClientManager creates a client manager from configured agents.
func NewClientManager(agents map[string]RemoteAgent) *ClientManager {
	return &ClientManager{
		agents: agents,
		cards:  make(map[string]json.RawMessage),
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// ListAgents returns sorted agent IDs.
func (m *ClientManager) ListAgents() []string {
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// GetAgent returns agent config by ID.
func (m *ClientManager) GetAgent(id string) (RemoteAgent, bool) {
	a, ok := m.agents[id]
	return a, ok
}

// Discover fetches the agent card from a remote agent.
func (m *ClientManager) Discover(ctx context.Context, agentID string) (string, error) {
	agent, ok := m.agents[agentID]
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agentID)
	}

	url := strings.TrimRight(agent.URL, "/") + "/.well-known/agent-card.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.client.Do(req) //nolint:gosec // agent URL is configured
	if err != nil {
		return "", fmt.Errorf("discover %s: %w", agentID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discover %s: HTTP %d", agentID, resp.StatusCode)
	}

	m.mu.Lock()
	m.cards[agentID] = json.RawMessage(body)
	m.mu.Unlock()

	return string(body), nil
}

// Call sends a message to a remote agent and returns the response text.
func (m *ClientManager) Call(ctx context.Context, agentID, message string) (string, error) {
	agent, ok := m.agents[agentID]
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agentID)
	}

	endpoint := strings.TrimRight(agent.URL, "/") + "/a2a"

	// Build JSON-RPC request.
	msgID := fmt.Sprintf("dozor-%d", time.Now().UnixMilli())
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "message/send",
		"id":      msgID,
		"params": map[string]any{
			"message": map[string]any{
				"messageId": msgID,
				"role":      "user",
				"parts":     []map[string]any{{"kind": "text", "text": message}},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if agent.Token != "" {
		req.Header.Set("Authorization", "Bearer "+agent.Token)
	}

	slog.Info("calling remote agent",
		slog.String("agent_id", agentID),
		slog.Int("message_len", len(message)))
	resp, err := m.client.Do(req) //nolint:gosec // agent URL is configured
	if err != nil {
		return "", fmt.Errorf("call %s: %w", agentID, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("call %s: HTTP %d: %s", agentID, resp.StatusCode, string(respBody))
	}

	// Parse JSON-RPC response → extract text from result.
	return extractResponseText(respBody)
}

// extractResponseText pulls text content from an A2A JSON-RPC response.
func extractResponseText(data []byte) (string, error) {
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &rpc); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("remote error: %s", rpc.Error.Message)
	}

	// The result is a Task object. Extract text from status.message.parts.
	var task struct {
		Status struct {
			State   string `json:"state"`
			Message struct {
				Parts []struct {
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rpc.Result, &task); err != nil {
		return "", fmt.Errorf("parse task: %w", err)
	}

	if task.Status.State == "failed" {
		return "", errors.New("remote task failed")
	}

	var texts []string
	for _, p := range task.Status.Message.Parts {
		if p.Kind == "text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	if len(texts) == 0 {
		return "", errors.New("empty response from agent")
	}
	return strings.Join(texts, ""), nil
}

// ParseAgentsEnv parses DOZOR_A2A_AGENTS env var format:
// "orchestrator=http://host:port,devops=http://host:port"
// Optional per-agent tokens via DOZOR_A2A_AGENT_<NAME>_TOKEN env vars.
func ParseAgentsEnv(agentsStr string) map[string]RemoteAgent {
	agents := make(map[string]RemoteAgent)
	if agentsStr == "" {
		return agents
	}
	for _, entry := range strings.Split(agentsStr, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		url := strings.TrimSpace(parts[1])
		if id == "" || url == "" {
			continue
		}
		agents[id] = RemoteAgent{URL: url, Alias: id}
	}
	return agents
}
