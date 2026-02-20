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
		services := agent.ResolveUserServices(ctx)
		if len(services) == 0 {
			return nil, engine.TextOutput{}, fmt.Errorf("no user services found (auto-discovery found none, or set DOZOR_USER_SERVICES in .env)")
		}

		switch input.Action {
		case "status":
			return nil, engine.TextOutput{Text: userServicesStatus(ctx, agent, input.Service, services)}, nil

		case "restart":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service name is required for restart action")
			}
			if engine.FindUserServiceIn(services, input.Service) == nil {
				return nil, engine.TextOutput{}, fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.UserServiceNamesFrom(services), ", "))
			}
			return nil, engine.TextOutput{Text: userServiceRestart(ctx, agent, input.Service)}, nil

		case "restart-all":
			return nil, engine.TextOutput{Text: userServicesRestartAll(ctx, agent, services)}, nil

		case "logs":
			if input.Service == "" {
				return nil, engine.TextOutput{}, fmt.Errorf("service name is required for logs action")
			}
			if engine.FindUserServiceIn(services, input.Service) == nil {
				return nil, engine.TextOutput{}, fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.UserServiceNamesFrom(services), ", "))
			}
			lines := input.Lines
			if lines <= 0 {
				lines = 50
			}
			if lines > 5000 {
				lines = 5000
			}
			return nil, engine.TextOutput{Text: userServiceLogs(ctx, agent, input.Service, lines)}, nil

		default:
			return nil, engine.TextOutput{}, fmt.Errorf("unknown action %q, use: status, restart, restart-all, logs", input.Action)
		}
	})
}

func userServicesStatus(ctx context.Context, agent *engine.ServerAgent, singleService string, allServices []engine.UserService) string {
	services := allServices
	if singleService != "" {
		svc := engine.FindUserServiceIn(allServices, singleService)
		if svc == nil {
			return fmt.Sprintf("Unknown service %q, available: %s", singleService, strings.Join(engine.UserServiceNamesFrom(allServices), ", "))
		}
		services = []engine.UserService{*svc}
	}

	user := agent.GetConfig().UserServicesUser
	if user == "" {
		user = "auto-discovered"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "User Services [%s] (%d)\n\n", user, len(services))

	for _, svc := range services {
		res := agent.ExecuteCommand(ctx, fmt.Sprintf("systemctl --user is-active %s", svc.Name))
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

		res = agent.ExecuteCommand(ctx, fmt.Sprintf("systemctl --user show %s --property=ActiveEnterTimestamp,MemoryCurrent", svc.Name))
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

func userServiceRestart(ctx context.Context, agent *engine.ServerAgent, service string) string {
	res := agent.ExecuteCommand(ctx, fmt.Sprintf("systemctl --user restart %s", service))
	if !res.Success {
		return fmt.Sprintf("Failed to restart %s: %s", service, res.Output())
	}
	res = agent.ExecuteCommand(ctx, fmt.Sprintf("systemctl --user is-active %s", service))
	state := strings.TrimSpace(res.Stdout)
	if state == "active" {
		return fmt.Sprintf("Service %s restarted successfully (active).", service)
	}
	return fmt.Sprintf("Service %s restarted but state is: %s", service, state)
}

func userServicesRestartAll(ctx context.Context, agent *engine.ServerAgent, services []engine.UserService) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Restarting %d services...\n\n", len(services))
	for _, svc := range services {
		result := userServiceRestart(ctx, agent, svc.Name)
		fmt.Fprintf(&b, "%s\n", result)
	}
	return b.String()
}

func userServiceLogs(ctx context.Context, agent *engine.ServerAgent, service string, lines int) string {
	res := agent.ExecuteCommand(ctx, fmt.Sprintf("journalctl --user-unit %s --no-pager -n %d", service, lines))
	if !res.Success {
		return fmt.Sprintf("Failed to get logs for %s: %s", service, res.Output())
	}
	output := res.Output()
	if output == "" {
		return fmt.Sprintf("No logs found for %s", service)
	}
	return fmt.Sprintf("Logs for %s (last %d lines):\n\n%s", service, lines, output)
}
