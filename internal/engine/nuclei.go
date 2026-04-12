package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NucleiFinding represents a vulnerability finding from nuclei scan.
type NucleiFinding struct {
	TemplateID       string     `json:"template-id"`
	TemplatePath     string     `json:"template-path"`
	Info             NucleiInfo `json:"info"`
	Type             string     `json:"type"`
	Host             string     `json:"host"`
	MatchedAt        string     `json:"matched-at"`
	ExtractedResults []string   `json:"extracted-results,omitempty"`
	IP               string     `json:"ip,omitempty"`
	Timestamp        time.Time  `json:"timestamp"`
	MatcherStatus    bool       `json:"matcher-status"`
	MatcherName      string     `json:"matcher-name,omitempty"`
}

// NucleiInfo contains metadata about the template.
type NucleiInfo struct {
	Name        string   `json:"name"`
	Authors     []string `json:"authors"`
	Tags        []string `json:"tags"`
	Severity    string   `json:"severity"`
	Description string   `json:"description,omitempty"`
	Reference   []string `json:"reference,omitempty"`
}

// ToAlertLevel converts nuclei severity to dozor alert level.
func (f NucleiFinding) ToAlertLevel() AlertLevel {
	switch strings.ToLower(f.Info.Severity) {
	case "critical":
		return AlertCritical
	case "high":
		return AlertError
	case "medium":
		return AlertWarning
	case "low", "info":
		return AlertInfo
	default:
		return AlertWarning
	}
}

// ToSecurityIssue converts a nuclei finding to a security issue.
func (f NucleiFinding) ToSecurityIssue() SecurityIssue {
	return SecurityIssue{
		Level:       f.ToAlertLevel(),
		Category:    "vulnerability",
		Title:       fmt.Sprintf("[%s] %s", f.Info.Severity, f.Info.Name),
		Description: f.Info.Description,
		Remediation: fmt.Sprintf("See template %s. Reference: %v", f.TemplateID, f.Info.Reference),
		Evidence:    fmt.Sprintf("Host: %s, Matched: %s", f.Host, f.MatchedAt),
	}
}

// NucleiCollector runs nuclei vulnerability scans.
type NucleiCollector struct {
	transport *Transport
	cfg       Config
}

// NewNucleiCollector creates a new nuclei collector.
func NewNucleiCollector(t *Transport, cfg Config) *NucleiCollector {
	return &NucleiCollector{transport: t, cfg: cfg}
}

// IsAvailable checks if nuclei is installed.
func (n *NucleiCollector) IsAvailable(ctx context.Context) bool {
	res := n.transport.Execute(ctx, "which nuclei")
	return res.Success && strings.TrimSpace(res.Stdout) != ""
}

// ScanTarget scans a single target (URL or host).
func (n *NucleiCollector) ScanTarget(ctx context.Context, target string) ([]NucleiFinding, error) {
	if !n.IsAvailable(ctx) {
		return nil, errors.New("nuclei not installed")
	}

	// Build nuclei command with JSONL output
	cmd := fmt.Sprintf("nuclei -u %s -silent -jsonl -timeout 30 -max-host-error 30", target)

	// Add severity filter if configured
	if n.cfg.NucleiSeverities != "" {
		cmd += " -severity " + n.cfg.NucleiSeverities
	}

	res := n.transport.Execute(ctx, cmd)
	if !res.Success {
		return nil, fmt.Errorf("nuclei scan failed: %s", res.Stderr)
	}

	return n.parseFindings(res.Stdout)
}

// ScanServices scans exposed web services from service statuses.
func (n *NucleiCollector) ScanServices(ctx context.Context, services []ServiceStatus) ([]NucleiFinding, error) {
	var allFindings []NucleiFinding

	for _, svc := range services {
		// Only scan services with healthcheck URLs or known web ports
		target := n.getScanTarget(svc)
		if target == "" {
			continue
		}

		findings, err := n.ScanTarget(ctx, target)
		if err != nil {
			// Log error but continue scanning other services
			continue
		}
		allFindings = append(allFindings, findings...)
	}

	return allFindings, nil
}

// getScanTarget determines the scan target for a service.
func (n *NucleiCollector) getScanTarget(svc ServiceStatus) string {
	// Use healthcheck URL if available
	if svc.HealthcheckURL != "" {
		return svc.HealthcheckURL
	}

	// For common service names, use default ports
	switch svc.Name {
	case "n8n", "n8n-server":
		return "http://localhost:5678"
	case "hasura", "hasura-graphql":
		return "http://localhost:8080"
	case "supabase-kong", "kong":
		return "http://localhost:8000"
	case "supabase-studio":
		return "http://localhost:3000"
	}

	return ""
}

// parseFindings parses nuclei JSONL output.
func (n *NucleiCollector) parseFindings(output string) ([]NucleiFinding, error) {
	var findings []NucleiFinding
	scanner := bufio.NewScanner(strings.NewReader(output))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var finding NucleiFinding
		if err := json.Unmarshal([]byte(line), &finding); err != nil {
			// Skip malformed lines
			continue
		}
		findings = append(findings, finding)
	}

	if err := scanner.Err(); err != nil {
		return findings, fmt.Errorf("error scanning output: %w", err)
	}

	return findings, nil
}

// FindingsToIssues converts nuclei findings to security issues.
func FindingsToIssues(findings []NucleiFinding) []SecurityIssue {
	issues := make([]SecurityIssue, len(findings))
	for i, f := range findings {
		issues[i] = f.ToSecurityIssue()
	}
	return issues
}
