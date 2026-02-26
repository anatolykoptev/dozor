package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerProbe(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_probe",
		Description: `Check HTTP/HTTPS endpoints for availability, response time, and SSL certificate expiry.
Probes all URLs concurrently. Use to verify services are reachable before/after deploys or during incidents.
Modes:
- http (default): probe URLs for HTTP status, latency, SSL. Set check_headers=true to audit security headers.
- dns: resolve hostnames and show A, AAAA, CNAME, MX records.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ProbeInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if len(input.URLs) == 0 {
			return nil, engine.TextOutput{}, errors.New("at least one URL is required")
		}
		if len(input.URLs) > maxProbeURLs {
			return nil, engine.TextOutput{}, fmt.Errorf("maximum %d URLs per probe request", maxProbeURLs)
		}
		for _, u := range input.URLs {
			if u == "" {
				return nil, engine.TextOutput{}, errors.New("empty URL in list")
			}
		}

		mode := input.Mode
		if mode == "" {
			mode = "http"
		}

		switch mode {
		case "http":
			results := agent.ProbeURLs(ctx, input.URLs, input.TimeoutS, input.CheckHeaders)
			return nil, engine.TextOutput{Text: engine.FormatProbeResults(results)}, nil

		case "dns":
			hostnames := make([]string, len(input.URLs))
			for i, u := range input.URLs {
				hostnames[i] = engine.ExtractHostname(u)
			}
			results := agent.ProbeDNS(ctx, hostnames)
			return nil, engine.TextOutput{Text: engine.FormatDNSResults(results)}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown probe mode %q, use: http, dns", mode)
		}
	})
}
