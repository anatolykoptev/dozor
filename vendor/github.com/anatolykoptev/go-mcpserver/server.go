package mcpserver

import (
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates an *mcp.Server with ServerOptions derived from cfg
// (KeepAlive, SchemaCache, SupportedProtocolVersions). MCP middleware
// (ToolTimeout, ToolFilter, custom receiving/sending) is applied later by
// Run/Build — NewServer only handles the options that must be set at creation
// time. These are the NewServer-only options; Run/Build log a warning if they
// are set on their Config. HTTP-handler options such as MaxRequestBodyBytes
// are not NewServer-only: Run, Build and Serve all honour them.
//
// SupportedProtocolVersions panics at startup, naming the offending value and
// the valid set, when it lists a protocol version the linked go-sdk does not
// implement.
//
// Use this instead of mcp.NewServer(impl, nil) to get KeepAlive, SchemaCache
// and SupportedProtocolVersions support. If you don't need those, mcp.NewServer(impl, nil)
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
	if len(cfg.SupportedProtocolVersions) > 0 {
		validateProtocolVersions(cfg.SupportedProtocolVersions)
		opts.SupportedProtocolVersions = cfg.SupportedProtocolVersions
	}
	return opts
}

// validateProtocolVersions panics with a clear message when versions holds an
// entry the linked go-sdk does not implement. Called from NewServer, i.e. at
// startup, where a misconfiguration should fail loudly rather than be ignored.
func validateProtocolVersions(versions []string) {
	valid := mcp.SupportedProtocolVersions()
	for _, v := range versions {
		if !slices.Contains(valid, v) {
			panic(fmt.Sprintf("mcpserver: Config.SupportedProtocolVersions contains unknown protocol version %q; valid values: %v", v, valid))
		}
	}
}
