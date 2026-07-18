package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates an *mcp.Server with ServerOptions derived from cfg
// (KeepAlive, SchemaCache). MCP middleware (ToolTimeout, ToolFilter, custom
// receiving/sending) is applied later by Run/Build — NewServer only handles
// the options that must be set at creation time.
//
// Use this instead of mcp.NewServer(impl, nil) to get KeepAlive and
// SchemaCache support. If you don't need those, mcp.NewServer(impl, nil)
// + mcpserver.Run(server, cfg) still works (Run applies middleware).
//
//	cfg := mcpserver.Config{
//	    Name:        "my-service",
//	    Version:     "1.0.0",
//	    KeepAlive:   30 * time.Second,
//	    SchemaCache: mcp.NewSchemaCache(), // share across servers in stateless mode
//	}
//	server := mcpserver.NewServer(&mcp.Implementation{Name: "my-service", Version: "1.0.0"}, cfg)
//	mcpserver.AddTool(server, &mcp.Tool{Name: "ping"}, handler)
//	if err := mcpserver.Run(server, cfg); err != nil { ... }
func NewServer(impl *mcp.Implementation, cfg Config) *mcp.Server {
	cfg = withDefaults(cfg)
	opts := buildServerOptions(cfg)
	return mcp.NewServer(impl, opts)
}

// buildServerOptions translates Config fields into *mcp.ServerOptions.
// Only includes fields that go-mcpserver exposes; consumer can create
// ServerOptions directly for fields we don't cover yet.
func buildServerOptions(cfg Config) *mcp.ServerOptions {
	opts := &mcp.ServerOptions{}
	if cfg.KeepAlive > 0 {
		opts.KeepAlive = cfg.KeepAlive
	}
	if cfg.SchemaCache != nil {
		opts.SchemaCache = cfg.SchemaCache
	}
	return opts
}
