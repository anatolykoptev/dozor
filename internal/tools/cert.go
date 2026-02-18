package tools

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCert(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_cert",
		Description: `Scan and display TLS/SSL certificates found on the server.
Checks: /etc/letsencrypt/live/, Caddy storage, /etc/nginx/ssl/, and common locations.
Actions: list (show all), check (list with expiry warnings).`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CertInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		switch input.Action {
		case "list", "check", "":
			warnDays := input.WarnDays
			if warnDays <= 0 {
				warnDays = 30
			}
			certs := agent.ScanCerts(ctx)
			return nil, engine.TextOutput{Text: engine.FormatCerts(certs, warnDays)}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown action %q, use: list, check", input.Action)
		}
	})
}
