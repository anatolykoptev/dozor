package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// alertsDefaultSince is the default lookback window when Since is not specified.
const alertsDefaultSince = time.Hour

// alertsDefaultLimit is the default cap on ring entries returned when Limit is not specified.
// The engine ring has its own independent default (engine.defaultRecentLimit); this tool
// always passes an explicit limit, so the ring default is never reached from this path.
const alertsDefaultLimit = 50

// AlertsActiveInput is the input schema for the alerts-active MCP tool.
type AlertsActiveInput struct {
	// Since controls the lookback window for ring (non-Prometheus) alerts.
	// Accepts Prometheus duration strings: 1h, 30m, 2h. Default: 1h.
	Since string `json:"since,omitempty" jsonschema:"Lookback window for ring (non-Prometheus) alerts as a Go duration string e.g. 1h 30m 2h. Calendar units like 1d/1w are not accepted; use 24h. Default: 1h."`

	// Limit caps the number of ring entries returned. Default: 50.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum ring entries to return. Default: 50."`

	// ExcludeFiring omits the live Alertmanager call when true. Default: false (firing alerts are included).
	ExcludeFiring bool `json:"exclude_firing,omitempty" jsonschema:"Set true to skip the live Alertmanager query and return only ring (non-Prometheus) alerts."`
}

// AlertsActiveOutput is the output schema for the alerts-active MCP tool.
type AlertsActiveOutput struct {
	// Firing holds currently-active alerts from Alertmanager (/api/v2/alerts?active=true).
	Firing []FiringAlert `json:"firing"`

	// Recent holds alerts delivered to Telegram by dozor's non-Prometheus sources
	// (mechanical watch, monitor-script healthchecks, deploy failures) within the
	// Since window, newest-first. Cleared on dozor restart.
	Recent []engine.AlertRecord `json:"recent"`

	// Warnings collects non-fatal issues (e.g. Alertmanager unreachable) that
	// caused partial results. The tool never fails just because one source is down.
	Warnings []string `json:"warnings,omitempty"`

	// Verdict is a one-line summary, e.g. "2 firing, 5 recent (1h)".
	Verdict string `json:"verdict"`
}

// registerAlerts wires the alerts-active MCP tool into the server.
//
// The tool has two complementary sources:
//  1. Live Prometheus-firing alerts from Alertmanager (GET /api/v2/alerts?active=true).
//  2. A dozor-internal in-memory ring of alerts already delivered to Telegram via
//     notifyAlertFn. This captures mechanical-watch, monitor-script, and deploy-fail
//     alerts that are otherwise fire-and-forget (not retained in any persistent store).
//
// Alertmanager failures are folded into Warnings, not returned as errors, so callers
// always get the ring data even when Prometheus is unreachable.
func registerAlerts(server *mcp.Server, _ *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "alerts-active",
		Description: `Returns ALL alerts that reached Telegram in one call.

Two sources are merged:
  1. LIVE: currently-firing Prometheus alerts from Alertmanager (always fresh).
  2. RING: recently-delivered non-Prometheus alerts from dozor's in-memory ring
     (mechanical watch, monitor-script healthchecks, deploy failures). These are
     fire-and-forget — the ring is the only retained record after Telegram delivery.
     Cleared on dozor restart.

Examples:
  {}                                           — last 1h ring, include firing
  {"since":"30m"}                              — last 30m ring only
  {"since":"2h","limit":100}                   — wider window, higher cap
  {"exclude_firing":true}                      — ring-only, skip Alertmanager call`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input AlertsActiveInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		out, err := handleAlertsActive(ctx, input)
		if err != nil {
			return nil, engine.TextOutput{}, err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return nil, engine.TextOutput{Text: string(b)}, nil
	})
}

// handleAlertsActive is the core logic, separated for testing.
func handleAlertsActive(ctx context.Context, input AlertsActiveInput) (*AlertsActiveOutput, error) {
	// Parse since duration (Go duration: s/m/h — calendar units like 1d/1w are
	// not accepted; use 24h/168h).
	since := alertsDefaultSince
	if input.Since != "" {
		d, err := time.ParseDuration(input.Since)
		if err != nil {
			return nil, fmt.Errorf("invalid since %q: %w", input.Since, err)
		}
		since = d
	}

	limit := input.Limit
	if limit <= 0 {
		limit = alertsDefaultLimit
	}

	out := &AlertsActiveOutput{
		Firing: []FiringAlert{},
		Recent: []engine.AlertRecord{},
	}

	// --- Source 1: live Alertmanager firing alerts ---
	if !input.ExcludeFiring {
		alertURL := os.Getenv("DOZOR_ALERTMANAGER_URL")
		if alertURL == "" {
			alertURL = "http://127.0.0.1:9093"
		}
		firing, warn := fetchFiringAlerts(ctx, alertURL)
		if warn != "" {
			out.Warnings = append(out.Warnings, "alertmanager: "+warn)
		}
		if firing != nil {
			out.Firing = firing
		}
	}

	// --- Source 2: ring of non-Prometheus alerts ---
	if recs := engine.DefaultAlertRing.Recent(since, limit); recs != nil {
		out.Recent = recs
	}

	// Build verdict.
	sinceLabel := input.Since
	if sinceLabel == "" {
		sinceLabel = "1h"
	}
	out.Verdict = fmt.Sprintf("%d firing, %d recent (%s)", len(out.Firing), len(out.Recent), sinceLabel)

	return out, nil
}
