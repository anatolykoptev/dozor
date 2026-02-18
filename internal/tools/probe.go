package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerProbe(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_probe",
		Description: `Check HTTP/HTTPS endpoints for availability, response time, and SSL certificate expiry.
Probes all URLs concurrently. Use to verify services are reachable before/after deploys or during incidents.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ProbeInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if len(input.URLs) == 0 {
			return nil, engine.TextOutput{}, fmt.Errorf("at least one URL is required")
		}
		if len(input.URLs) > 20 {
			return nil, engine.TextOutput{}, fmt.Errorf("maximum 20 URLs per probe request")
		}
		for _, u := range input.URLs {
			if u == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("empty URL in list")
			}
		}
		results := agent.ProbeURLs(ctx, input.URLs, input.TimeoutS)
		return nil, engine.TextOutput{Text: engine.FormatProbeResults(results)}, nil
	})
}
