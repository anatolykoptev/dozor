package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/approvals"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ExecOptions holds optional dependencies for the exec tool.
type ExecOptions struct {
	// Config holds the mutable default security mode. Created via NewExecConfig().
	Config *ExecConfig
	// Approvals manager for security=ask mode. May be nil (disables ask mode).
	Approvals *approvals.Manager
	// Notify sends an async message to the admin (e.g. Telegram). May be nil.
	Notify func(string)
}

func registerExec(server *mcp.Server, agent *engine.ServerAgent, opts ExecOptions) {
	cfg := opts.Config
	if cfg == nil {
		cfg = NewExecConfig()
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "server_exec",
		Description: "Execute a shell command on the server. " +
			"Security modes: safe (default — blocks dangerous patterns), " +
			"ask (sends command to Telegram for user approval before executing), " +
			"full (unrestricted — use with caution).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ExecInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if input.Command == "" {
			return nil, engine.TextOutput{}, errors.New("command is required")
		}

		security := cfg.Get()
		if s := strings.ToLower(strings.TrimSpace(input.Security)); s != "" {
			security = s
		}

		switch security {
		case "full":
			// No checks — execute directly.

		case "ask":
			// Request user approval via Telegram before executing.
			if opts.Approvals == nil || opts.Notify == nil {
				return nil, engine.TextOutput{}, errors.New("security=ask unavailable: no Telegram notify channel configured")
			}
			req := opts.Approvals.Create(input.Command)
			opts.Notify(fmt.Sprintf(
				"⚠️ *Command execution request*\n```\n%s\n```\nReply:\n✅ `yes %s`\n❌ `no %s`",
				input.Command, req.ID, req.ID,
			))
			switch opts.Approvals.Wait(req, approvals.DefaultTimeout) {
			case approvals.StatusApproved:
				// approved — fall through to execution
			case approvals.StatusDenied:
				return nil, engine.TextOutput{
					Text: "Command rejected by user: " + input.Command,
				}, nil
			default: // StatusExpired
				return nil, engine.TextOutput{}, fmt.Errorf(
					"approval timed out (2 min) for: %s", input.Command)
			}

		default: // "safe"
			if ok, reason := engine.IsCommandAllowed(input.Command); !ok {
				return nil, engine.TextOutput{}, fmt.Errorf("command blocked: %s", reason)
			}
		}

		result := agent.ExecuteCommand(ctx, input.Command)
		if !result.Success {
			return nil, engine.TextOutput{
				Text: fmt.Sprintf("Command failed (exit %d):\n%s", result.ReturnCode, result.Output()),
			}, nil
		}
		return nil, engine.TextOutput{Text: result.Output()}, nil
	})
}
