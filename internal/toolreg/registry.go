package toolreg

import (
	"context"
	"fmt"
	"sync"

	"github.com/anatolykoptev/dozor/internal/provider"
)

// Tool is the interface that all agent-callable tools implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any // JSON Schema object
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// Registry holds all registered tools.
type Registry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Execute runs a tool by name with the given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// List returns all tool names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// ToLLMTools converts all registered tools to OpenAI-compatible tool definitions.
func (r *Registry) ToLLMTools() []provider.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]provider.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, provider.ToolDefinition{
			Type: "function",
			Function: provider.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return defs
}
