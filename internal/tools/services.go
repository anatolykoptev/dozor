package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerServices(server *mcp.Server, agent *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_services",
		Description: `Manage user-level systemd services. Actions:
- status: show all services or one service (active/inactive, memory, uptime, port)
- restart: restart a specific service
- restart-all: restart all configured services
- logs: show recent logs for a service`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ServicesInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		cfg := agent.GetConfig()
		if !cfg.HasUserServices() {
			return nil, engine.TextOutput{}, fmt.Errorf("no user services configured (set DOZOR_USER_SERVICES and DOZOR_USER_SERVICES_USER in .env)")
		}

		switch input.Action {
		case "status":
			return nil, engine.TextOutput{Text: userServicesStatus(ctx, cfg, agent, input.Service)}, nil

		case "restart":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service name is required for restart action")
			}
			if cfg.FindUserService(input.Service) == nil {
				return nil, engine.TextOutput{}, fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(cfg.UserServiceNames(), ", "))
			}
			return nil, engine.TextOutput{Text: userServiceRestart(ctx, cfg, agent, input.Service)}, nil

		case "restart-all":
			return nil, engine.TextOutput{Text: userServicesRestartAll(ctx, cfg, agent)}, nil

		case "logs":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service name is required for logs action")
			}
			if cfg.FindUserService(input.Service) == nil {
				return nil, engine.TextOutput{}, fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(cfg.UserServiceNames(), ", "))
			}
			lines := input.Lines
			if lines <= 0 {
				lines = 50
			}
			if lines > 5000 {
				lines = 5000
			}
			return nil, engine.TextOutput{Text: userServiceLogs(ctx, cfg, agent, input.Service, lines)}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown action %q, use: status, restart, restart-all, logs", input.Action)
		}
	})
}

// userCmd builds a systemctl --user command.
func userCmd(_ engine.Config, command string) string {
	return fmt.Sprintf("systemctl --user %s", command)
}

// userJournalCmd builds a journalctl --user-unit command.
func userJournalCmd(_ engine.Config, unit string, lines int) string {
	return fmt.Sprintf("journalctl --user-unit %s --no-pager -n %d", unit, lines)
}

func userServicesStatus(ctx context.Context, cfg engine.Config, agent *engine.ServerAgent, singleService string) string {
	services := cfg.UserServices
	if singleService != "" {
		svc := cfg.FindUserService(singleService)
		if svc == nil {
			return fmt.Sprintf("Unknown service %q, available: %s", singleService, strings.Join(cfg.UserServiceNames(), ", "))
		}
		services = []engine.UserService{*svc}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "User Services [%s] (%d)\n\n", cfg.UserServicesUser, len(services))

	for _, svc := range services {
		cmd := userCmd(cfg, fmt.Sprintf("is-active %s", svc.Name))
		res := agent.ExecuteCommand(ctx, cmd)
		state := strings.TrimSpace(res.Stdout)
		if state == "" {
			state = strings.TrimSpace(res.Stderr)
		}
		if state == "" {
			state = "unknown"
		}

		icon := "OK"
		if state != "active" {
			icon = "!!"
		}

		portInfo := ""
		if svc.Port > 0 {
			portInfo = fmt.Sprintf(" (port %d)", svc.Port)
		}
		fmt.Fprintf(&b, "[%s] %s: %s%s\n", icon, svc.Name, state, portInfo)

		// Get memory and uptime
		cmd = userCmd(cfg, fmt.Sprintf("show %s --property=ActiveEnterTimestamp,MemoryCurrent", svc.Name))
		res = agent.ExecuteCommand(ctx, cmd)
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ActiveEnterTimestamp=") {
				ts := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
				if ts != "" {
					fmt.Fprintf(&b, "  Started: %s\n", ts)
				}
			}
			if strings.HasPrefix(line, "MemoryCurrent=") {
				mem := strings.TrimPrefix(line, "MemoryCurrent=")
				if mem != "" && mem != "[not set]" && mem != "18446744073709551615" {
					if mb, ok := engine.FormatBytesMB(mem); ok {
						fmt.Fprintf(&b, "  Memory: %s\n", mb)
					}
				}
			}
		}
	}

	return b.String()
}

func userServiceRestart(ctx context.Context, cfg engine.Config, agent *engine.ServerAgent, service string) string {
	cmd := userCmd(cfg, fmt.Sprintf("restart %s", service))
	res := agent.ExecuteCommand(ctx, cmd)
	if !res.Success {
		return fmt.Sprintf("Failed to restart %s: %s", service, res.Output())
	}

	// Verify it started
	cmd = userCmd(cfg, fmt.Sprintf("is-active %s", service))
	res = agent.ExecuteCommand(ctx, cmd)
	state := strings.TrimSpace(res.Stdout)

	if state == "active" {
		return fmt.Sprintf("Service %s restarted successfully (active).", service)
	}
	return fmt.Sprintf("Service %s restarted but state is: %s", service, state)
}

func userServicesRestartAll(ctx context.Context, cfg engine.Config, agent *engine.ServerAgent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Restarting %d services...\n\n", len(cfg.UserServices))

	for _, svc := range cfg.UserServices {
		result := userServiceRestart(ctx, cfg, agent, svc.Name)
		fmt.Fprintf(&b, "%s\n", result)
	}

	return b.String()
}

func userServiceLogs(ctx context.Context, cfg engine.Config, agent *engine.ServerAgent, service string, lines int) string {
	cmd := userJournalCmd(cfg, service, lines)
	res := agent.ExecuteCommand(ctx, cmd)
	if !res.Success {
		return fmt.Sprintf("Failed to get logs for %s: %s", service, res.Output())
	}
	output := res.Output()
	if output == "" {
		return fmt.Sprintf("No logs found for %s", service)
	}
	return fmt.Sprintf("Logs for %s (last %d lines):\n\n%s", service, lines, output)
}
