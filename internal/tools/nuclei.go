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

		// Validate caller input before any command runs.
		if input.Target != "" {
			if ok, reason := engine.ValidateScanTarget(input.Target); !ok {
				engine.LogRejectedInput("vuln_scan_target", reason, input.Target)
				return nil, engine.TextOutput{}, errors.New(reason)
			}
		}
		if ok, reason := engine.ValidateNucleiSeverities(input.Severities); !ok {
			engine.LogRejectedInput("vuln_scan_severities", reason, input.Severities)
			return nil, engine.TextOutput{}, errors.New(reason)
		}

		// Create nuclei collector on the agent's own transport
		collector := agent.NewNucleiCollector(cfg)

		// Check if nuclei is available
		if !collector.IsAvailable(ctx) {
			return nil, engine.TextOutput{}, errors.New("nuclei not installed. Install: go install github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest")
		}

		// Set severity filter
		if input.Severities != "" {
			cfg.NucleiSeverities = input.Severities
			collector = agent.NewNucleiCollector(cfg)
		}

		var findings []engine.NucleiFinding
		var skipped []string
		var err error

		if input.Target != "" {
			// Scan specific target
			findings, err = collector.ScanTarget(ctx, input.Target)
		} else {
			// Scan all configured services
			report := agent.Diagnose(ctx, nil)
			findings, skipped, err = collector.ScanServicesReport(ctx, report.Services)
		}

		if err != nil {
			return nil, engine.TextOutput{}, fmt.Errorf("scan failed: %w", err)
		}

		// Format results
		text := formatNucleiResults(findings, skipped)
		return nil, engine.TextOutput{Text: text}, nil
	})
}

// formatNucleiResults formats nuclei findings for display.
func formatNucleiResults(findings []engine.NucleiFinding, skipped []string) string {
	if len(findings) == 0 {
		if len(skipped) == 0 {
			return "No vulnerabilities found."
		}
		return "No vulnerabilities found in the targets that were scanned.\n" + formatSkippedTargets(skipped)
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

	if len(skipped) > 0 {
		b.WriteString(formatSkippedTargets(skipped))
	}
	return b.String()
}

// formatSkippedTargets lists targets that were not scanned, so they are not
// mistaken for clean ones.
func formatSkippedTargets(skipped []string) string {
	return fmt.Sprintf("Skipped %d target(s), NOT scanned:\n  - %s\n", len(skipped), strings.Join(skipped, "\n  - "))
}
