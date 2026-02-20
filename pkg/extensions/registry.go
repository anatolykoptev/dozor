package extensions

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registry manages extension loading and lifecycle.
type Registry struct {
	extensions map[string]Extension
	order      []string // insertion order for deterministic loading
	started    map[string]bool
	errors     []*ExtensionError
	mu         sync.RWMutex
}

// NewRegistry creates a new extension registry.
func NewRegistry() *Registry {
	return &Registry{
		extensions: make(map[string]Extension),
		started:    make(map[string]bool),
	}
}

// Register adds an extension to the registry in insertion order.
func (r *Registry) Register(ext Extension) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := ext.Name()
	if _, exists := r.extensions[name]; !exists {
		r.order = append(r.order, name)
	}
	r.extensions[name] = ext
}

// LoadAll registers and starts all extensions, collecting non-fatal errors.
// An optional notify func can be passed; it will be available to extensions via Context.Notify.
func (r *Registry) LoadAll(ctx context.Context, agent *engine.ServerAgent, tools *toolreg.Registry, mcpServer *mcp.Server, notify ...func(string)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	config := agent.GetConfig()

	var notifyFn func(string)
	if len(notify) > 0 {
		notifyFn = notify[0]
	}

	// Register phase — each extension gets its own scoped runtime
	registered := make([]string, 0, len(r.order))
	for _, name := range r.order {
		ext := r.extensions[name]
		runtime := NewRuntime(name)
		extCtx := &Context{
			Agent:     agent,
			Config:    &config,
			Tools:     tools,
			MCPServer: mcpServer,
			Runtime:   runtime,
			Notify:    notifyFn,
		}

		if err := r.registerOne(ctx, name, ext, extCtx); err != nil {
			r.errors = append(r.errors, err)
			runtime.Logger.Warn("extension skipped", slog.String("phase", string(err.Phase)), slog.Any("error", err.Err))
			continue
		}
		registered = append(registered, name)

		// Start phase immediately after successful registration
		if err := r.startOne(ctx, name, ext, extCtx); err != nil {
			r.errors = append(r.errors, err)
			runtime.Logger.Warn("extension start failed", slog.Any("error", err.Err))
			continue
		}
		r.started[name] = true
	}

	slog.Info("extensions loaded",
		slog.Int("total", len(r.order)),
		slog.Int("started", len(r.started)),
		slog.Int("failed", len(r.errors)))

	return nil
}

// registerOne runs the validation + register phase for one extension.
func (r *Registry) registerOne(ctx context.Context, name string, ext Extension, extCtx *Context) *ExtensionError {
	// Rich config validation (OpenClaw pattern)
	if configExt, ok := ext.(ConfigurableExtension); ok {
		result := configExt.ValidateConfig(extCtx.Config)
		if !result.OK {
			if err := result.Error(); err != nil {
				return wrapError(name, PhaseValidate, err)
			}
		}
		caps := configExt.GetCapabilities()
		extCtx.Runtime.Logger.Debug("extension capabilities",
			slog.Bool("tools", caps.Tools),
			slog.Bool("mcp_tools", caps.MCPTools),
			slog.Bool("lifecycle", caps.Lifecycle))
	}

	if err := ext.Register(ctx, extCtx); err != nil {
		return wrapError(name, PhaseRegister, err)
	}

	extCtx.Runtime.Logger.Debug("extension registered")
	return nil
}

// startOne runs the start phase for one extension.
func (r *Registry) startOne(ctx context.Context, name string, ext Extension, extCtx *Context) *ExtensionError {
	if startExt, ok := ext.(StartableExtension); ok {
		if err := startExt.Start(ctx, extCtx); err != nil {
			return wrapError(name, PhaseStart, err)
		}
		extCtx.Runtime.Logger.Debug("extension started")
	}
	return nil
}

// Stop gracefully shuts down all running extensions in reverse order.
func (r *Registry) Stop(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Reverse order shutdown
	reversed := make([]string, len(r.order))
	copy(reversed, r.order)
	sort.Sort(sort.Reverse(sort.StringSlice(reversed)))

	for _, name := range reversed {
		if !r.started[name] {
			continue
		}
		ext := r.extensions[name]
		if stopExt, ok := ext.(StoppableExtension); ok {
			if err := stopExt.Stop(ctx); err != nil {
				slog.Warn("extension stop failed",
					slog.String("extension", name),
					slog.Any("error", err))
			}
		}
		r.started[name] = false
	}
}

// List returns extension names in registration order.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, len(r.order))
	copy(result, r.order)
	return result
}

// Errors returns all non-fatal errors encountered during loading.
func (r *Registry) Errors() []*ExtensionError {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.errors
}
