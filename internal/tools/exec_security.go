package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ExecSecurityInput struct {
	Action   string `json:"action" jsonschema:"Action: get (show current mode), set (change mode)"`
	Security string `json:"security,omitempty" jsonschema:"Security mode to set: safe (blocks dangerous commands), ask (requires Telegram approval), full (unrestricted)"`
}

func registerExecSecurity(server *mcp.Server, cfg *ExecConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_exec_security",
		Description: "Get or set the default security mode for server_exec. " +
			"Modes: safe (minimal blocklist), ask (Telegram approval required), full (unrestricted). " +
			"Changes take effect immediately for all subsequent server_exec calls.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input ExecSecurityInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		switch strings.ToLower(strings.TrimSpace(input.Action)) {
		case "get", "":
			return nil, engine.TextOutput{
				Text: fmt.Sprintf("Current exec security mode: %s\nAvailable modes: %s",
					cfg.Get(), strings.Join(ValidSecurityModes, ", ")),
			}, nil

		case "set":
			if err := cfg.Set(input.Security); err != nil {
				return nil, engine.TextOutput{}, err
			}
			return nil, engine.TextOutput{
				Text: "Exec security mode set to: " + cfg.Get(),
			}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf(
				"unknown action %q: use get or set", input.Action)
		}
	})
}
