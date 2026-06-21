package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tgMessagesDefaultSince is the default lookback window when Since is empty.
const tgMessagesDefaultSince = 6 * time.Hour

// tgMessagesDefaultLimit is the default cap on returned messages when Limit <= 0.
const tgMessagesDefaultLimit = 100

// TGMessagesInput is the input schema for the tg-messages MCP tool.
type TGMessagesInput struct {
	// Since is the lookback window as a Go duration string (s/m/h). Default: 6h.
	// Calendar units like 1d/1w are not accepted; use 24h/168h.
	Since string `json:"since,omitempty" jsonschema:"Lookback window as a Go duration string e.g. 6h 30m 24h. Calendar units (1d/1w) are not accepted; use 24h. Default: 6h."`

	// Limit caps the number of messages returned. Default: 100.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of messages to return. Default: 100."`

	// Kind filters by message type: alert, notify, reply, ack, session, deploy, other.
	// Empty means return all kinds.
	Kind string `json:"kind,omitempty" jsonschema:"Optional kind filter: alert notify reply ack session deploy other. Empty returns all kinds."`
}

// TGMessagesOutput is the output schema for the tg-messages MCP tool.
type TGMessagesOutput struct {
	// Messages is the list of Telegram messages in newest-first order.
	Messages []engine.TGMessage `json:"messages"`

	// Verdict is a one-line summary, e.g. "12 messages (6h, kind=all)".
	Verdict string `json:"verdict"`
}

// registerTGMessages wires the tg-messages MCP tool into the server.
//
// The tool exposes the durable TGMessageLog as a queryable history of all
// Telegram traffic from dozor — alerts, replies, acks, deploy notifications, etc.
// It complements alerts-active (which is the structured live view); this tool
// is the durable delivery record including replies and non-alert traffic.
func registerTGMessages(server *mcp.Server, _ *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "tg-messages",
		Description: `Durable history of ALL messages dozor sent to Telegram, surviving process restart.

Captures every outbound message: alerts (photo cards + text), agent replies, acks,
session messages, deploy notifications. Photo bytes are not stored; only HasPhoto=true
marks their presence. Text is truncated at 4000 characters.

Kinds: alert, notify, reply, ack, session, deploy, other.

Examples:
  {}                                   — last 6h, all kinds, up to 100 messages
  {"since":"1h"}                       — last 1h window
  {"kind":"alert","since":"24h"}       — only alert messages in last 24h
  {"kind":"reply","limit":20}          — last 20 agent reply messages`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input TGMessagesInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		out, err := handleTGMessages(input)
		if err != nil {
			return nil, engine.TextOutput{}, err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return nil, engine.TextOutput{Text: string(b)}, nil
	})
}

// handleTGMessages is the core logic, separated for testing.
func handleTGMessages(input TGMessagesInput) (*TGMessagesOutput, error) {
	since := tgMessagesDefaultSince
	if input.Since != "" {
		d, err := time.ParseDuration(input.Since)
		if err != nil {
			return nil, fmt.Errorf("invalid since %q: %w", input.Since, err)
		}
		since = d
	}

	limit := input.Limit
	if limit <= 0 {
		limit = tgMessagesDefaultLimit
	}

	msgs := engine.DefaultTGLog.Recent(since, limit, input.Kind)
	if msgs == nil {
		msgs = []engine.TGMessage{}
	}

	kindLabel := input.Kind
	if kindLabel == "" {
		kindLabel = "all"
	}
	sinceLabel := input.Since
	if sinceLabel == "" {
		sinceLabel = "6h"
	}

	return &TGMessagesOutput{
		Messages: msgs,
		Verdict:  fmt.Sprintf("%d messages (%s, kind=%s)", len(msgs), sinceLabel, kindLabel),
	}, nil
}
