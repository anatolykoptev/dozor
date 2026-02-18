package extensions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterIntrospectTool adds a server_extensions MCP tool to the given server.
// It provides runtime visibility into loaded extensions, their status, and config hints.
func (r *Registry) RegisterIntrospectTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_extensions",
		Description: "List loaded Dozor extensions with their status, capabilities, and configuration hints.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{ Text string }, error) {
		out := r.introspectJSON()
		return nil, struct{ Text string }{Text: out}, nil
	})
}

type extensionStatus struct {
	Name         string              `json:"name"`
	Started      bool                `json:"started"`
	Capabilities *Capabilities       `json:"capabilities,omitempty"`
	ConfigHints  map[string]ConfigHint `json:"config_hints,omitempty"`
	Error        string              `json:"error,omitempty"`
}

func (r *Registry) introspectJSON() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Build error index
	errIdx := make(map[string]string)
	for _, e := range r.errors {
		errIdx[e.Extension] = fmt.Sprintf("[%s] %v", e.Phase, e.Err)
	}

	statuses := make([]extensionStatus, 0, len(r.order))
	for _, name := range r.order {
		ext := r.extensions[name]
		status := extensionStatus{
			Name:    name,
			Started: r.started[name],
		}
		if configExt, ok := ext.(ConfigurableExtension); ok {
			caps := configExt.GetCapabilities()
			status.Capabilities = &caps
			status.ConfigHints = configExt.GetConfigHints()
		}
		if errMsg, ok := errIdx[name]; ok {
			status.Error = errMsg
		}
		statuses = append(statuses, status)
	}

	b, _ := json.MarshalIndent(statuses, "", "  ")
	return string(b)
}
