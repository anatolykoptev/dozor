package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerNuclei(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_vuln_scan",
		Description: `Run vulnerability scan using Nuclei on exposed services.
Scans web services for known CVEs, misconfigurations, and exposed sensitive endpoints.
Uses nuclei templates (8000+) from ProjectDiscovery.

Examples:
- Scan all configured services: no arguments
- Scan specific URL: target="http://localhost:8080"
- Only critical vulnerabilities: severities="critical"

Note: Nuclei must be installed on the server. Install: go install github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.NucleiInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		cfg := agent.GetConfig()

		// Create nuclei collector
		t := engine.NewTransport(cfg)
		collector := engine.NewNucleiCollector(t, cfg)

		// Check if nuclei is available
		if !collector.IsAvailable(ctx) {
			return nil, engine.TextOutput{}, errors.New("nuclei not installed. Install: go install github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest")
		}

		// Set severity filter
		if input.Severities != "" {
			cfg.NucleiSeverities = input.Severities
			collector = engine.NewNucleiCollector(t, cfg)
		}

		var findings []engine.NucleiFinding
		var err error

		if input.Target != "" {
			// Scan specific target
			findings, err = collector.ScanTarget(ctx, input.Target)
		} else {
			// Scan all configured services
			report := agent.Diagnose(ctx, nil)
			findings, err = collector.ScanServices(ctx, report.Services)
		}

		if err != nil {
			return nil, engine.TextOutput{}, fmt.Errorf("scan failed: %w", err)
		}

		// Format results
		text := formatNucleiResults(findings)
		return nil, engine.TextOutput{Text: text}, nil
	})
}

// formatNucleiResults formats nuclei findings for display.
func formatNucleiResults(findings []engine.NucleiFinding) string {
	if len(findings) == 0 {
		return "No vulnerabilities found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d vulnerability(s):\n\n", len(findings))

	for i, f := range findings {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, f.Info.Severity, f.Info.Name)
		fmt.Fprintf(&b, "   Template: %s\n", f.TemplateID)
		fmt.Fprintf(&b, "   Host: %s\n", f.Host)
		if f.MatchedAt != "" && f.MatchedAt != f.Host {
			fmt.Fprintf(&b, "   Matched: %s\n", f.MatchedAt)
		}
		if f.Info.Description != "" {
			fmt.Fprintf(&b, "   Description: %s\n", f.Info.Description)
		}
		if len(f.Info.Reference) > 0 {
			fmt.Fprintf(&b, "   References: %s\n", strings.Join(f.Info.Reference, ", "))
		}
		if len(f.ExtractedResults) > 0 {
			fmt.Fprintf(&b, "   Extracted: %s\n", strings.Join(f.ExtractedResults, ", "))
		}
		b.WriteString("\n")
	}

	return b.String()
}
