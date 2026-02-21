package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientManager manages connections to remote MCP servers.
type ClientManager struct {
	servers  map[string]engine.MCPServerConfig
	clients  map[string]*mcp.Client
	sessions map[string]*mcp.ClientSession
	mu       sync.RWMutex
}

// NewClientManager creates a client manager from configured servers.
func NewClientManager(servers map[string]engine.MCPServerConfig) *ClientManager {
	return &ClientManager{
		servers:  servers,
		clients:  make(map[string]*mcp.Client),
		sessions: make(map[string]*mcp.ClientSession),
	}
}

// ListServers returns sorted server IDs.
func (m *ClientManager) ListServers() []string {
	ids := make([]string, 0, len(m.servers))
	for id := range m.servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// GetServer returns server config by ID.
func (m *ClientManager) GetServer(id string) (engine.MCPServerConfig, bool) {
	s, ok := m.servers[id]
	return s, ok
}

// getSession returns existing session or creates new one.
func (m *ClientManager) getSession(ctx context.Context, serverID string) (*mcp.ClientSession, error) {
	m.mu.RLock()
	if session, ok := m.sessions[serverID]; ok {
		m.mu.RUnlock()
		return session, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double check
	if session, ok := m.sessions[serverID]; ok {
		return session, nil
	}

	server, ok := m.servers[serverID]
	if !ok {
		return nil, fmt.Errorf("unknown server: %s", serverID)
	}

	// Create client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "dozor-mcp-client",
		Version: "1.0.0",
	}, nil)

	// Connect via Streamable HTTP transport
	transport := &mcp.StreamableClientTransport{
		Endpoint: server.URL,
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", serverID, err)
	}

	m.clients[serverID] = client
	m.sessions[serverID] = session

	slog.Info("mcp client connected",
		slog.String("server_id", serverID),
		slog.String("url", server.URL))

	return session, nil
}

// Discover fetches the list of tools from a remote MCP server.
func (m *ClientManager) Discover(ctx context.Context, serverID string) ([]ToolInfo, error) {
	session, err := m.getSession(ctx, serverID)
	if err != nil {
		return nil, err
	}

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools from %s: %w", serverID, err)
	}

	tools := make([]ToolInfo, 0, len(result.Tools))
	for _, t := range result.Tools {
		tools = append(tools, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
		})
	}

	return tools, nil
}

// ToolInfo describes a remote tool.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Call invokes a tool on a remote MCP server.
func (m *ClientManager) Call(ctx context.Context, serverID, toolName string, params map[string]any) (string, error) {
	session, err := m.getSession(ctx, serverID)
	if err != nil {
		return "", err
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: params,
	})
	if err != nil {
		return "", fmt.Errorf("call %s.%s: %w", serverID, toolName, err)
	}

	// Extract text content from result
	var textParts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			textParts = append(textParts, text.Text)
		}
	}

	if len(textParts) == 0 {
		// Return raw result if no text content
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data), nil
	}

	return strings.Join(textParts, "\n"), nil
}

// Close closes all client sessions.
func (m *ClientManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, session := range m.sessions {
		if err := session.Close(); err != nil {
			slog.Warn("close session", slog.String("server_id", id), slog.Any("error", err))
		}
	}

	m.sessions = make(map[string]*mcp.ClientSession)
	m.clients = make(map[string]*mcp.Client)
}
