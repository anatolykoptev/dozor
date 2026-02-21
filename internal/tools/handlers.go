package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// Handler functions implement the core tool logic.
// Both MCP tools and the toolreg bridge delegate to these.

// HandleInspect processes a server_inspect request.
func HandleInspect(ctx context.Context, agent *engine.ServerAgent, input engine.InspectInput) (string, error) {
	switch input.Mode {
	case "health":
		return agent.GetHealth(ctx), nil

	case "status":
		if input.Service == "" {
			return "", fmt.Errorf("service is required for status mode")
		}
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return "", fmt.Errorf("invalid service: %s", reason)
		}
		return engine.FormatStatus(agent.GetServiceStatus(ctx, input.Service)), nil

	case "diagnose":
		report := agent.Diagnose(ctx, input.Services)
		text := engine.FormatReport(report)
		if report.NeedsAttention() {
			data, _ := json.MarshalIndent(report, "", "  ")
			text += "\n\n<diagnostic_data>\n" + string(data) + "\n</diagnostic_data>"
		}
		return text, nil

	case "logs":
		if input.Service == "" {
			return "", fmt.Errorf("service is required for logs mode")
		}
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return "", fmt.Errorf("invalid service: %s", reason)
		}
		lines := input.Lines
		if lines <= 0 {
			lines = 100
		}
		if lines > 10000 {
			return "", fmt.Errorf("lines must be <= 10000")
		}
		entries := agent.GetLogs(ctx, input.Service, lines, false)
		filter := strings.ToLower(input.Filter)
		var b strings.Builder
		matched := 0
		for _, e := range entries {
			if filter != "" {
				haystack := strings.ToLower(e.Message + e.Raw)
				if !strings.Contains(haystack, filter) {
					continue
				}
			}
			if e.Timestamp != nil {
				fmt.Fprintf(&b, "[%s] ", e.Timestamp.Format("15:04:05"))
			}
			fmt.Fprintf(&b, "[%s] %s\n", e.Level, e.Message)
			matched++
		}
		header := fmt.Sprintf("Logs for %s (%d entries", input.Service, matched)
		if filter != "" {
			header += fmt.Sprintf(", filter=%q", input.Filter)
		}
		header += "):\n\n"
		return header + b.String(), nil

	case "analyze":
		if input.Service == "" {
			return agent.AnalyzeAll(ctx), nil
		}
		if ok, reason := engine.ValidateServiceName(input.Service); !ok {
			return "", fmt.Errorf("invalid service: %s", reason)
		}
		entries := agent.GetLogs(ctx, input.Service, 1000, false)
		result := engine.AnalyzeLogs(entries, input.Service)
		return engine.FormatAnalysisEnriched(result, entries), nil

	case "errors":
		return agent.GetAllErrors(ctx), nil

	case "security":
		return engine.FormatSecurityReport(agent.CheckSecurity(ctx)), nil

	case "overview":
		return agent.GetOverview(ctx), nil

	case "remote":
		return agent.GetRemoteStatus(ctx), nil

	case "systemd":
		return agent.GetSystemdStatus(ctx, input.Services), nil

	case "connections":
		return agent.GetConnections(ctx), nil

	case "cron":
		return agent.GetScheduledTasks(ctx), nil

	default:
		return "", fmt.Errorf("unknown mode %q, use: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd, connections, cron", input.Mode)
	}
}

// HandleTriage processes a server_triage request.
func HandleTriage(ctx context.Context, agent *engine.ServerAgent, input engine.TriageInput) (string, error) {
	for _, svc := range input.Services {
		if ok, reason := engine.ValidateServiceName(svc); !ok {
			return "", fmt.Errorf("invalid service %q: %s", svc, reason)
		}
	}
	return agent.Triage(ctx, input.Services), nil
}

// HandleExecSafe processes a server_exec request in "safe" mode only.
// The full MCP exec handler also supports "ask" and "full" modes.
func HandleExecSafe(ctx context.Context, agent *engine.ServerAgent, command string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if ok, reason := engine.IsCommandAllowed(command); !ok {
		return "", fmt.Errorf("command blocked: %s", reason)
	}
	result := agent.ExecuteCommand(ctx, command)
	if !result.Success {
		return fmt.Sprintf("Command failed (exit %d):\n%s", result.ReturnCode, result.Output()), nil
	}
	return result.Output(), nil
}

// HandleRemoteExec processes a server_remote_exec request.
func HandleRemoteExec(ctx context.Context, agent *engine.ServerAgent, command string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if ok, reason := engine.IsCommandAllowed(command); !ok {
		return "", fmt.Errorf("command blocked: %s", reason)
	}
	result := agent.RemoteExec(ctx, command)
	if !result.Success {
		return fmt.Sprintf("Command failed (exit %d):\n%s", result.ReturnCode, result.Output()), nil
	}
	return result.Output(), nil
}

// HandleRestart processes a server_restart request.
func HandleRestart(ctx context.Context, agent *engine.ServerAgent, service string) (string, error) {
	if ok, reason := engine.ValidateServiceName(service); !ok {
		return "", fmt.Errorf("invalid service: %s", reason)
	}
	result := agent.RestartService(ctx, service)
	if !result.Success {
		return fmt.Sprintf("Restart failed: %s", result.Output()), nil
	}
	return fmt.Sprintf("Service %s restarted successfully.", service), nil
}

// HandleDeploy processes a server_deploy request.
func HandleDeploy(ctx context.Context, agent *engine.ServerAgent, input engine.DeployInput) (string, error) {
	if input.Action == "health" {
		return agent.CheckDeployHealth(ctx, input.Services), nil
	}

	if input.DeployID != "" || input.Action == "status" {
		if input.DeployID == "" {
			return "", fmt.Errorf("deploy_id is required for status action")
		}
		if ok, reason := engine.ValidateDeployID(input.DeployID); !ok {
			return "", fmt.Errorf("invalid deploy ID: %s", reason)
		}
		status := agent.GetDeployStatus(ctx, input.DeployID)
		text := fmt.Sprintf("Deploy: %s\nStatus: %s\nProcess running: %v\nLog file: %s\n",
			input.DeployID, status.Status, status.ProcessRunning, status.LogFile)
		if status.LogContent != "" {
			lines := status.LogContent
			if len(lines) > 3000 {
				lines = "...\n" + lines[len(lines)-3000:]
			}
			text += "\nLog output:\n" + lines
		}
		return text, nil
	}

	build := true
	if input.Build != nil {
		build = *input.Build
	}
	pull := true
	if input.Pull != nil {
		pull = *input.Pull
	}

	result := agent.StartDeploy(ctx, input.ProjectPath, input.Services, build, pull)
	if !result.Success {
		return "", fmt.Errorf("deploy failed: %s", result.Error)
	}
	return fmt.Sprintf("Deploy started.\nID: %s\nLog: %s\n\nCheck status: server_deploy({deploy_id: %q})\nVerify health: server_deploy({action: \"health\"})",
		result.DeployID, result.LogFile, result.DeployID), nil
}

// HandlePrune processes a server_prune request.
func HandlePrune(ctx context.Context, agent *engine.ServerAgent, input engine.PruneInput) (string, error) {
	images := true
	if input.Images != nil {
		images = *input.Images
	}
	buildCache := true
	if input.BuildCache != nil {
		buildCache = *input.BuildCache
	}
	volumes := false
	if input.Volumes != nil {
		volumes = *input.Volumes
	}
	age := input.Age
	if age == "" {
		age = "24h"
	}
	if ok, reason := engine.ValidateTimeDuration(age); !ok {
		return "", fmt.Errorf("invalid age: %s", reason)
	}
	return agent.PruneDocker(ctx, images, buildCache, volumes, age), nil
}

// HandleCleanup processes a server_cleanup request.
func HandleCleanup(ctx context.Context, agent *engine.ServerAgent, input engine.CleanupInput) (string, error) {
	if len(input.Targets) > 0 {
		if ok, reason := engine.ValidateCleanupTargets(input.Targets); !ok {
			return "", fmt.Errorf("invalid targets: %s", reason)
		}
	}
	if input.MinAge != "" {
		if ok, reason := engine.ValidateTimeDuration(input.MinAge); !ok {
			return "", fmt.Errorf("invalid min_age: %s", reason)
		}
	}
	report := true
	if input.Report != nil {
		report = *input.Report
	}
	return agent.CleanupSystem(ctx, input.Targets, report, input.MinAge), nil
}

// HandleServices processes a server_services request.
func HandleServices(ctx context.Context, agent *engine.ServerAgent, input engine.ServicesInput) (string, error) {
	services := agent.ResolveUserServices(ctx)
	if len(services) == 0 {
		return "", fmt.Errorf("no user services found (auto-discovery found none, or set DOZOR_USER_SERVICES in .env)")
	}

	switch input.Action {
	case "status":
		return userServicesStatus(ctx, agent, input.Service, services), nil
	case "restart":
		if input.Service == "" {
			return "", fmt.Errorf("service name is required for restart action")
		}
		if engine.FindUserServiceIn(services, input.Service) == nil {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.UserServiceNamesFrom(services), ", "))
		}
		return userServiceRestart(ctx, agent, input.Service), nil
	case "restart-all":
		return userServicesRestartAll(ctx, agent, services), nil
	case "logs":
		if input.Service == "" {
			return "", fmt.Errorf("service name is required for logs action")
		}
		if engine.FindUserServiceIn(services, input.Service) == nil {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.UserServiceNamesFrom(services), ", "))
		}
		lines := input.Lines
		if lines <= 0 {
			lines = 50
		}
		if lines > 5000 {
			lines = 5000
		}
		return userServiceLogs(ctx, agent, input.Service, lines), nil
	default:
		return "", fmt.Errorf("unknown action %q, use: status, restart, restart-all, logs", input.Action)
	}
}

// HandleUpdates processes a server_updates request.
func HandleUpdates(ctx context.Context, agent *engine.ServerAgent, input engine.UpdatesInput) (string, error) {
	switch input.Action {
	case "check":
		return engine.FormatUpdatesCheck(agent.CheckUpdates(ctx)), nil
	case "install":
		if input.Binary == "" {
			return "", fmt.Errorf("binary name is required for install action")
		}
		if ok, reason := engine.ValidateBinaryName(input.Binary); !ok {
			return "", fmt.Errorf("invalid binary name: %s", reason)
		}
		return agent.InstallUpdate(ctx, input.Binary)
	default:
		return "", fmt.Errorf("unknown action %q, use: check, install", input.Action)
	}
}

// HandleRemote processes a server_remote request.
func HandleRemote(ctx context.Context, agent *engine.ServerAgent, input engine.RemoteInput) (string, error) {
	cfg := agent.GetConfig()
	if !cfg.HasRemote() || len(cfg.RemoteServices) == 0 {
		return "", fmt.Errorf("no remote services configured (set DOZOR_REMOTE_HOST and DOZOR_REMOTE_SERVICES in .env)")
	}

	switch input.Action {
	case "status":
		return engine.RemoteServiceStatus(ctx, cfg), nil
	case "restart":
		if input.Service == "" {
			return "", fmt.Errorf("service name is required for restart action")
		}
		if !engine.IsValidRemoteService(cfg, input.Service) {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
		}
		return engine.RemoteRestart(ctx, cfg, input.Service), nil
	case "logs":
		if input.Service == "" {
			return "", fmt.Errorf("service name is required for logs action")
		}
		if !engine.IsValidRemoteService(cfg, input.Service) {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
		}
		lines := input.Lines
		if lines <= 0 {
			lines = 50
		}
		if lines > 5000 {
			lines = 5000
		}
		return engine.RemoteLogs(ctx, cfg, input.Service, lines), nil
	default:
		return "", fmt.Errorf("unknown action %q, use: status, restart, logs", input.Action)
	}
}
