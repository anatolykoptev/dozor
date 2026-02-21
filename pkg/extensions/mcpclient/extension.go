package mcpclient

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/mcpclient"
	"github.com/anatolykoptev/dozor/pkg/extensions"
)

// MCPClientExtension provides remote MCP server connectivity.
type MCPClientExtension struct {
	manager *mcpclient.ClientManager
}

func New() *MCPClientExtension { return &MCPClientExtension{} }

func (e *MCPClientExtension) Name() string { return "mcpclient" }

func (e *MCPClientExtension) GetCapabilities() extensions.Capabilities {
	return extensions.Capabilities{
		Tools:     true,
		MCPTools:  false,
		Config:    true,
		Lifecycle: false,
	}
}

func (e *MCPClientExtension) ValidateConfig(config *engine.Config) extensions.ConfigValidation {
	var errs []extensions.ConfigError

	if len(config.MCPServers) == 0 {
		errs = append(errs, extensions.ConfigError{
			Field:   "DOZOR_MCP_SERVERS",
			Message: "no remote MCP servers configured",
		})
	}
	for id, server := range config.MCPServers {
		if server.URL == "" {
			errs = append(errs, extensions.ConfigError{
				Field:   "DOZOR_MCP_SERVERS",
				Message: "server " + id + " has empty URL",
			})
		}
	}

	return extensions.ConfigValidation{OK: len(errs) == 0, Errors: errs}
}

func (e *MCPClientExtension) GetConfigHints() map[string]extensions.ConfigHint {
	return map[string]extensions.ConfigHint{
		"DOZOR_MCP_SERVERS": {
			Label:       "Remote MCP Servers",
			Help:        "Comma-separated list of id=url pairs, e.g. go_search=http://127.0.0.1:8890/mcp",
			Placeholder: "go_search=http://127.0.0.1:8890/mcp",
		},
	}
}

func (e *MCPClientExtension) Register(ctx context.Context, extCtx *extensions.Context) error {
	log := extCtx.Runtime.Logger

	if len(extCtx.Config.MCPServers) == 0 {
		log.Info("no MCP servers configured, skipping")
		return nil
	}

	servers := make(map[string]engine.MCPServerConfig)
	for id, server := range extCtx.Config.MCPServers {
		servers[id] = server
	}

	e.manager = mcpclient.NewClientManager(servers)

	if extCtx.Tools != nil {
		mcpclient.RegisterTools(extCtx.Tools, e.manager)
		mcpclient.RegisterMemDBTools(extCtx.Tools, e.manager, mcpclient.MemDBConfig{
			ServerID: "memdb",
			UserID:   extCtx.Config.MemDBUser,
			CubeID:   extCtx.Config.MemDBCube,
		})
	}

	log.Info("mcp client registered", "servers", len(servers))
	return nil
}
