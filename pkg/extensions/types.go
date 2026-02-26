package extensions

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Extension provides capabilities to Dozor runtime.
// Based on Vaelor/PicoClaw/OpenClaw patterns.
type Extension interface {
	Name() string
	Register(ctx context.Context, extCtx *Context) error
}

// Context is shared with extensions during registration.
// Provides access to all Dozor subsystems and a scoped logger.
type Context struct {
	Agent     *engine.ServerAgent
	Config    *engine.Config
	Tools     *toolreg.Registry // Agent tool registry
	MCPServer *mcp.Server       // MCP server for external tools
	Runtime   *Runtime          // Per-extension runtime (logging, workspace, http)
	Notify    func(string)      // Send async notification to admin (e.g. Telegram). May be nil.
}

// Runtime provides per-extension isolated runtime resources (OpenClaw pattern).
type Runtime struct {
	Logger    *slog.Logger
	WorkDir   string
	ConfigDir string
}

// NewRuntime creates a scoped runtime for an extension.
func NewRuntime(name string) *Runtime {
	return &Runtime{
		Logger: slog.Default().With(slog.String("extension", name)),
	}
}

// Capabilities declares what an extension provides (for introspection & filtering).
type Capabilities struct {
	Tools     bool `json:"tools"`      // Registers agent tools
	MCPTools  bool `json:"mcp_tools"`  // Registers MCP tools
	Config    bool `json:"config"`     // Requires configuration
	Lifecycle bool `json:"lifecycle"`  // Has start/stop lifecycle
}

// ConfigValidation is the result of config validation (OpenClaw pattern).
type ConfigValidation struct {
	OK     bool
	Errors []ConfigError
}

// ConfigError describes a single configuration error.
type ConfigError struct {
	Field   string
	Message string
}

func (v ConfigValidation) Error() error {
	if v.OK || len(v.Errors) == 0 {
		return nil
	}
	msg := "config validation failed:"
	for _, e := range v.Errors {
		msg += fmt.Sprintf(" [%s: %s]", e.Field, e.Message) //nolint:perfsprint
	}
	return fmt.Errorf("%s", msg)
}

// ConfigHint provides UI/documentation hints for a config field (OpenClaw pattern).
type ConfigHint struct {
	Label       string
	Help        string
	Sensitive   bool
	Required    bool
	Placeholder string
}

// ExtensionPhase identifies which lifecycle phase an error occurred in.
type ExtensionPhase string

const (
	PhaseValidate ExtensionPhase = "validate"
	PhaseRegister ExtensionPhase = "register"
	PhaseStart    ExtensionPhase = "start"
	PhaseStop     ExtensionPhase = "stop"
)

// ExtensionError wraps an error with phase and extension context (PicoClaw pattern).
type ExtensionError struct {
	Extension string
	Phase     ExtensionPhase
	Err       error
}

func (e *ExtensionError) Error() string {
	return fmt.Sprintf("extension %s [%s]: %v", e.Extension, e.Phase, e.Err)
}

func (e *ExtensionError) Unwrap() error { return e.Err }

// wrapError wraps an error with extension context.
func wrapError(name string, phase ExtensionPhase, err error) *ExtensionError {
	return &ExtensionError{Extension: name, Phase: phase, Err: err}
}

// --- Optional extension interfaces ---

// StartableExtension can be started after registration.
type StartableExtension interface {
	Start(ctx context.Context, extCtx *Context) error
}

// StoppableExtension can be gracefully shut down.
type StoppableExtension interface {
	Stop(ctx context.Context) error
}

// ConfigurableExtension validates config and declares capabilities.
type ConfigurableExtension interface {
	ValidateConfig(config *engine.Config) ConfigValidation
	GetCapabilities() Capabilities
	GetConfigHints() map[string]ConfigHint
}
