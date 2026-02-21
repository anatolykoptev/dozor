package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerDevMode(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_dev_mode",
		Description: `Toggle dev mode to prevent auto-fixing during active development. ` +
			`Dev mode makes the periodic watch observe-only (no restarts/modifications). ` +
			`You can also exclude specific services from triage entirely. ` +
			`Exclusions auto-expire after TTL (default 4h). ` +
			`Call with status=true (or no args) to see current state.`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.DevModeInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		ttl := 4 * time.Hour
		if input.TTL != "" {
			d, err := time.ParseDuration(input.TTL)
			if err != nil {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid ttl %q: %w", input.TTL, err)
			}
			ttl = d
		}

		// Apply changes
		if input.Enable != nil {
			agent.SetDevMode(*input.Enable)
		}
		for _, svc := range input.Exclude {
			if ok, reason := engine.ValidateServiceName(svc); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("invalid service %q: %s", svc, reason)
			}
			agent.ExcludeService(svc, ttl)
		}
		for _, svc := range input.Include {
			agent.IncludeService(svc)
		}

		// Build status response
		var b strings.Builder
		if agent.IsDevMode() {
			b.WriteString("Dev mode: ON (watch is observe-only)\n")
		} else {
			b.WriteString("Dev mode: OFF (watch takes corrective action)\n")
		}

		exclusions := agent.ListExclusions()
		if len(exclusions) > 0 {
			fmt.Fprintf(&b, "\nExcluded services (%d):\n", len(exclusions))
			for name, expiry := range exclusions {
				remaining := time.Until(expiry).Truncate(time.Minute)
				fmt.Fprintf(&b, "  %s — expires in %s\n", name, remaining)
			}
		} else {
			b.WriteString("\nNo service exclusions.\n")
		}

		return nil, engine.TextOutput{Text: b.String()}, nil
	})
}
