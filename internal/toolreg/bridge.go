package toolreg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// RegisterAll registers all engine methods as tools in the registry.
func RegisterAll(r *Registry, agent *engine.ServerAgent) {
	r.Register(&inspectTool{agent: agent})
	r.Register(&triageTool{agent: agent})
	r.Register(&execTool{agent: agent})
	r.Register(&remoteExecTool{agent: agent})
	r.Register(&restartTool{agent: agent})
	r.Register(&deployTool{agent: agent})
	r.Register(&pruneTool{agent: agent})
	r.Register(&cleanupTool{agent: agent})
	r.Register(&servicesTool{agent: agent})
	r.Register(&updatesTool{agent: agent})
	r.Register(&remoteTool{agent: agent})

	// Web tools moved to MCP-only (internal/tools/web.go)
}

// helpers

func getString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

func getBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func getStringSlice(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return s
	}
	return nil
}

// --- server_inspect ---

type inspectTool struct{ agent *engine.ServerAgent }

func (t *inspectTool) Name() string { return "server_inspect" }
func (t *inspectTool) Description() string {
	return `Inspect server state. Modes:
- health: quick OK/!! status of all services
- status: detailed status for one service (CPU, memory, uptime, errors)
- diagnose: full diagnostics with alerts and health assessment
- logs: recent logs for a service (supports line count)
- analyze: error pattern analysis with remediation suggestions
- errors: ERROR/FATAL log lines from all services in one call
- security: security audit (network, containers, auth, API hardening)
- overview: system dashboard (disk, memory, load, top processes, docker summary)
- remote: remote server monitoring (HTTP, SSL, systemd services via SSH)
- systemd: local systemd service monitoring`
}
func (t *inspectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode":     map[string]any{"type": "string", "description": "Inspection mode: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd"},
			"service":  map[string]any{"type": "string", "description": "Service name (required for status, logs modes; optional for analyze)"},
			"services": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Services to diagnose (all if omitted)"},
			"lines":    map[string]any{"type": "integer", "description": "Number of log lines (default 100, only for logs mode)"},
		},
		"required": []string{"mode"},
	}
}
func (t *inspectTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	mode := getString(args, "mode")
	service := getString(args, "service")
	services := getStringSlice(args, "services")
	lines := getInt(args, "lines", 100)

	switch mode {
	case "health":
		return t.agent.GetHealth(ctx), nil
	case "status":
		if service == "" {
			return "", fmt.Errorf("service is required for status mode")
		}
		if ok, reason := engine.ValidateServiceName(service); !ok {
			return "", fmt.Errorf("invalid service: %s", reason)
		}
		return engine.FormatStatus(t.agent.GetServiceStatus(ctx, service)), nil
	case "diagnose":
		report := t.agent.Diagnose(ctx, services)
		text := engine.FormatReport(report)
		if report.NeedsAttention() {
			data, _ := json.MarshalIndent(report, "", "  ")
			text += "\n\n<diagnostic_data>\n" + string(data) + "\n</diagnostic_data>"
		}
		return text, nil
	case "logs":
		if service == "" {
			return "", fmt.Errorf("service is required for logs mode")
		}
		if ok, reason := engine.ValidateServiceName(service); !ok {
			return "", fmt.Errorf("invalid service: %s", reason)
		}
		if lines > 10000 {
			return "", fmt.Errorf("lines must be <= 10000")
		}
		entries := t.agent.GetLogs(ctx, service, lines, false)
		if len(entries) > 50 {
			entries = entries[len(entries)-50:]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Logs for %s (%d entries):\n\n", service, len(entries))
		for _, e := range entries {
			if e.Timestamp != nil {
				fmt.Fprintf(&b, "[%s] ", e.Timestamp.Format("15:04:05"))
			}
			fmt.Fprintf(&b, "[%s] %s\n", e.Level, e.Message)
		}
		return b.String(), nil
	case "analyze":
		if service == "" {
			return t.agent.AnalyzeAll(ctx), nil
		}
		if ok, reason := engine.ValidateServiceName(service); !ok {
			return "", fmt.Errorf("invalid service: %s", reason)
		}
		return engine.FormatAnalysis(t.agent.AnalyzeLogs(ctx, service)), nil
	case "errors":
		return t.agent.GetAllErrors(ctx), nil
	case "security":
		return engine.FormatSecurityReport(t.agent.CheckSecurity(ctx)), nil
	case "overview":
		return t.agent.GetOverview(ctx), nil
	case "remote":
		return t.agent.GetRemoteStatus(ctx), nil
	case "systemd":
		return t.agent.GetSystemdStatus(ctx, services), nil
	default:
		return "", fmt.Errorf("unknown mode %q, use: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd", mode)
	}
}

// --- server_triage ---

type triageTool struct{ agent *engine.ServerAgent }

func (t *triageTool) Name() string        { return "server_triage" }
func (t *triageTool) Description() string {
	return "Full auto-diagnosis in one call. Discovers all services, checks health, and for problematic services automatically analyzes error patterns, shows recent errors, and suggests remediation. Includes disk pressure alerts."
}
func (t *triageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"services": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Specific services to triage (all if omitted)"},
		},
	}
}
func (t *triageTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	services := getStringSlice(args, "services")
	for _, svc := range services {
		if ok, reason := engine.ValidateServiceName(svc); !ok {
			return "", fmt.Errorf("invalid service %q: %s", svc, reason)
		}
	}
	return t.agent.Triage(ctx, services), nil
}

// --- server_exec ---

type execTool struct{ agent *engine.ServerAgent }

func (t *execTool) Name() string        { return "server_exec" }
func (t *execTool) Description() string {
	return "Execute a validated shell command on the server. Commands are checked against a blocklist for safety."
}
func (t *execTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command to execute"},
		},
		"required": []string{"command"},
	}
}
func (t *execTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	command := getString(args, "command")
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if ok, reason := engine.IsCommandAllowed(command); !ok {
		return "", fmt.Errorf("command blocked: %s", reason)
	}
	result := t.agent.ExecuteCommand(ctx, command)
	if !result.Success {
		return fmt.Sprintf("Command failed (exit %d):\n%s", result.ReturnCode, result.Output()), nil
	}
	return result.Output(), nil
}

// --- server_remote_exec ---

type remoteExecTool struct{ agent *engine.ServerAgent }

func (t *remoteExecTool) Name() string { return "server_remote_exec" }
func (t *remoteExecTool) Description() string {
	return "Execute a validated shell command on the remote server via SSH. Commands are checked against a blocklist for safety."
}
func (t *remoteExecTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command to execute on the remote server via SSH"},
		},
		"required": []string{"command"},
	}
}
func (t *remoteExecTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	command := getString(args, "command")
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if ok, reason := engine.IsCommandAllowed(command); !ok {
		return "", fmt.Errorf("command blocked: %s", reason)
	}
	result := t.agent.RemoteExec(ctx, command)
	if !result.Success {
		return fmt.Sprintf("Command failed (exit %d):\n%s", result.ReturnCode, result.Output()), nil
	}
	return result.Output(), nil
}

// --- server_restart ---

type restartTool struct{ agent *engine.ServerAgent }

func (t *restartTool) Name() string        { return "server_restart" }
func (t *restartTool) Description() string { return "Restart a docker compose service." }
func (t *restartTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"service": map[string]any{"type": "string", "description": "Service to restart"},
		},
		"required": []string{"service"},
	}
}
func (t *restartTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	service := getString(args, "service")
	if ok, reason := engine.ValidateServiceName(service); !ok {
		return "", fmt.Errorf("invalid service: %s", reason)
	}
	result := t.agent.RestartService(ctx, service)
	if !result.Success {
		return fmt.Sprintf("Restart failed: %s", result.Output()), nil
	}
	return fmt.Sprintf("Service %s restarted successfully.", service), nil
}

// --- server_deploy ---

type deployTool struct{ agent *engine.ServerAgent }

func (t *deployTool) Name() string        { return "server_deploy" }
func (t *deployTool) Description() string {
	return "Deploy or check deploy status. If deploy_id is provided, checks existing deploy status. Otherwise starts a new background deployment (pulls, builds, docker compose up)."
}
func (t *deployTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"deploy_id":    map[string]any{"type": "string", "description": "Deploy ID to check status"},
			"project_path": map[string]any{"type": "string", "description": "Path to docker-compose project"},
			"services":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Specific services to deploy"},
			"build":        map[string]any{"type": "boolean", "description": "Build images before deploy (default true)"},
			"pull":         map[string]any{"type": "boolean", "description": "Pull images before deploy (default true)"},
		},
	}
}
func (t *deployTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	deployID := getString(args, "deploy_id")
	if deployID != "" {
		if ok, reason := engine.ValidateDeployID(deployID); !ok {
			return "", fmt.Errorf("invalid deploy ID: %s", reason)
		}
		status := t.agent.GetDeployStatus(ctx, deployID)
		text := fmt.Sprintf("Deploy: %s\nStatus: %s\nProcess running: %v\nLog file: %s\n",
			deployID, status.Status, status.ProcessRunning, status.LogFile)
		if status.LogContent != "" {
			lines := status.LogContent
			if len(lines) > 3000 {
				lines = "...\n" + lines[len(lines)-3000:]
			}
			text += "\nLog output:\n" + lines
		}
		return text, nil
	}

	projectPath := getString(args, "project_path")
	services := getStringSlice(args, "services")
	build := getBool(args, "build", true)
	pull := getBool(args, "pull", true)

	result := t.agent.StartDeploy(ctx, projectPath, services, build, pull)
	if !result.Success {
		return "", fmt.Errorf("deploy failed: %s", result.Error)
	}
	return fmt.Sprintf("Deploy started.\nID: %s\nLog: %s\n\nCheck status with server_deploy({deploy_id: %q}).",
		result.DeployID, result.LogFile, result.DeployID), nil
}

// --- server_prune ---

type pruneTool struct{ agent *engine.ServerAgent }

func (t *pruneTool) Name() string        { return "server_prune" }
func (t *pruneTool) Description() string {
	return "Clean up Docker resources (unused images, build cache, volumes). Shows disk usage after cleanup."
}
func (t *pruneTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"images":      map[string]any{"type": "boolean", "description": "Prune unused images (default true)"},
			"build_cache": map[string]any{"type": "boolean", "description": "Prune build cache (default true)"},
			"volumes":     map[string]any{"type": "boolean", "description": "Prune unused volumes (default false)"},
			"age":         map[string]any{"type": "string", "description": "Prune items older than (default 24h)"},
		},
	}
}
func (t *pruneTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	images := getBool(args, "images", true)
	buildCache := getBool(args, "build_cache", true)
	volumes := getBool(args, "volumes", false)
	age := getString(args, "age")
	if age == "" {
		age = "24h"
	}
	if ok, reason := engine.ValidateTimeDuration(age); !ok {
		return "", fmt.Errorf("invalid age: %s", reason)
	}
	return t.agent.PruneDocker(ctx, images, buildCache, volumes, age), nil
}

// --- server_cleanup ---

type cleanupTool struct{ agent *engine.ServerAgent }

func (t *cleanupTool) Name() string { return "server_cleanup" }
func (t *cleanupTool) Description() string {
	return "Scan or clean system resources to free disk space. Auto-detects: docker, go, npm, uv, pip, journal, tmp, caches. Default: dry-run (report=true) shows reclaimable sizes. Set report=false to execute cleanup."
}
func (t *cleanupTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"targets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Cleanup targets: docker, go, npm, uv, pip, journal, tmp, caches, all"},
			"report":  map[string]any{"type": "boolean", "description": "Dry-run: scan sizes without deleting (default true)"},
			"min_age": map[string]any{"type": "string", "description": "Minimum age for cleanup (e.g. 7d, 24h, 3d)"},
		},
	}
}
func (t *cleanupTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	targets := getStringSlice(args, "targets")
	if len(targets) > 0 {
		if ok, reason := engine.ValidateCleanupTargets(targets); !ok {
			return "", fmt.Errorf("invalid targets: %s", reason)
		}
	}
	minAge := getString(args, "min_age")
	if minAge != "" {
		if ok, reason := engine.ValidateTimeDuration(minAge); !ok {
			return "", fmt.Errorf("invalid min_age: %s", reason)
		}
	}
	report := getBool(args, "report", true)
	return t.agent.CleanupSystem(ctx, targets, report, minAge), nil
}

// --- server_services ---

type servicesTool struct{ agent *engine.ServerAgent }

func (t *servicesTool) Name() string { return "server_services" }
func (t *servicesTool) Description() string {
	return `Manage user-level systemd services. Actions:
- status: show all services or one service (active/inactive, memory, uptime, port)
- restart: restart a specific service
- restart-all: restart all configured services
- logs: show recent logs for a service`
}
func (t *servicesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":  map[string]any{"type": "string", "description": "Action: status, restart, logs, restart-all"},
			"service": map[string]any{"type": "string", "description": "Service name (required for restart, logs)"},
			"lines":   map[string]any{"type": "integer", "description": "Number of log lines (default 50, for logs action)"},
		},
		"required": []string{"action"},
	}
}
func (t *servicesTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	cfg := t.agent.GetConfig()
	if !cfg.HasUserServices() {
		return "", fmt.Errorf("no user services configured (set DOZOR_USER_SERVICES and DOZOR_USER_SERVICES_USER)")
	}

	action := getString(args, "action")
	service := getString(args, "service")
	lines := getInt(args, "lines", 50)

	switch action {
	case "status":
		return userServicesStatus(ctx, cfg, t.agent, service), nil
	case "restart":
		if service == "" {
			return "", fmt.Errorf("service name is required for restart action")
		}
		if cfg.FindUserService(service) == nil {
			return "", fmt.Errorf("unknown service %q, available: %s", service, strings.Join(cfg.UserServiceNames(), ", "))
		}
		return userServiceRestart(ctx, cfg, t.agent, service), nil
	case "restart-all":
		return userServicesRestartAll(ctx, cfg, t.agent), nil
	case "logs":
		if service == "" {
			return "", fmt.Errorf("service name is required for logs action")
		}
		if cfg.FindUserService(service) == nil {
			return "", fmt.Errorf("unknown service %q, available: %s", service, strings.Join(cfg.UserServiceNames(), ", "))
		}
		if lines > 5000 {
			lines = 5000
		}
		return userServiceLogs(ctx, cfg, t.agent, service, lines), nil
	default:
		return "", fmt.Errorf("unknown action %q, use: status, restart, restart-all, logs", action)
	}
}

// User service helpers (same logic as tools/services.go).

func userCmd(command string) string {
	return fmt.Sprintf("systemctl --user %s", command)
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
		cmd := userCmd(fmt.Sprintf("is-active %s", svc.Name))
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

		cmd = userCmd(fmt.Sprintf("show %s --property=ActiveEnterTimestamp,MemoryCurrent", svc.Name))
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
	cmd := userCmd(fmt.Sprintf("restart %s", service))
	res := agent.ExecuteCommand(ctx, cmd)
	if !res.Success {
		return fmt.Sprintf("Failed to restart %s: %s", service, res.Output())
	}
	cmd = userCmd(fmt.Sprintf("is-active %s", service))
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
	cmd := fmt.Sprintf("journalctl --user-unit %s --no-pager -n %d", service, lines)
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

// --- server_updates ---

type updatesTool struct{ agent *engine.ServerAgent }

func (t *updatesTool) Name() string { return "server_updates" }
func (t *updatesTool) Description() string {
	return "Check and install updates for CLI binaries installed from GitHub releases. Actions: check (scan binaries), install (download and replace binary)."
}
func (t *updatesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "description": "Action: check, install"},
			"binary": map[string]any{"type": "string", "description": "Binary name to install update for (required for install action)"},
		},
		"required": []string{"action"},
	}
}
func (t *updatesTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := getString(args, "action")
	switch action {
	case "check":
		return engine.FormatUpdatesCheck(t.agent.CheckUpdates(ctx)), nil
	case "install":
		binary := getString(args, "binary")
		if binary == "" {
			return "", fmt.Errorf("binary name is required for install action")
		}
		if ok, reason := engine.ValidateBinaryName(binary); !ok {
			return "", fmt.Errorf("invalid binary name: %s", reason)
		}
		return t.agent.InstallUpdate(ctx, binary)
	default:
		return "", fmt.Errorf("unknown action %q, use: check, install", action)
	}
}

// --- server_remote ---

type remoteTool struct{ agent *engine.ServerAgent }

func (t *remoteTool) Name() string { return "server_remote" }
func (t *remoteTool) Description() string {
	return `Manage remote server services (system-level systemd via sudo). Actions:
- status: show all remote services (active/inactive, uptime, memory)
- restart: restart a specific remote service
- logs: show recent journalctl logs for a remote service`
}
func (t *remoteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":  map[string]any{"type": "string", "description": "Action: status, restart, logs"},
			"service": map[string]any{"type": "string", "description": "Service name (required for restart and logs)"},
			"lines":   map[string]any{"type": "integer", "description": "Number of log lines (default 50, max 5000)"},
		},
		"required": []string{"action"},
	}
}
func (t *remoteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	cfg := t.agent.GetConfig()
	if !cfg.HasRemote() || len(cfg.RemoteServices) == 0 {
		return "", fmt.Errorf("no remote services configured (set DOZOR_REMOTE_HOST and DOZOR_REMOTE_SERVICES)")
	}

	action := getString(args, "action")
	service := getString(args, "service")
	lines := getInt(args, "lines", 50)

	switch action {
	case "status":
		return engine.RemoteServiceStatus(ctx, cfg), nil
	case "restart":
		if service == "" {
			return "", fmt.Errorf("service name is required for restart action")
		}
		if !engine.IsValidRemoteService(cfg, service) {
			return "", fmt.Errorf("unknown service %q, available: %s", service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
		}
		return engine.RemoteRestart(ctx, cfg, service), nil
	case "logs":
		if service == "" {
			return "", fmt.Errorf("service name is required for logs action")
		}
		if !engine.IsValidRemoteService(cfg, service) {
			return "", fmt.Errorf("unknown service %q, available: %s", service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
		}
		if lines > 5000 {
			lines = 5000
		}
		return engine.RemoteLogs(ctx, cfg, service, lines), nil
	default:
		return "", fmt.Errorf("unknown action %q, use: status, restart, logs", action)
	}
}
