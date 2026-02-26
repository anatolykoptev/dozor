package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
)

const (
	// modeStatus is the inspect mode for single-service status.
	modeStatus = "status"
	// modeLogs is the inspect mode for log streaming.
	modeLogs = "logs"
	// maxLinesLog is the maximum number of log lines allowed via inspect/logs mode.
	maxLinesLog = 10000
	// maxDeployLogChars is the maximum deploy log characters shown.
	maxDeployLogChars = 3000
	// maxLinesServices is the maximum log lines for services/remote handlers.
	maxLinesServices = 5000
	// maxProbeURLs is the maximum number of URLs for a probe request.
	maxProbeURLs = 20
)

// Handler functions implement the core tool logic.
// Both MCP tools and the toolreg bridge delegate to these.

// HandleInspect processes a server_inspect request.
//
//nolint:gocognit,cyclop // dispatch switch — complexity inherent in routing all inspect modes
func HandleInspect(ctx context.Context, agent *engine.ServerAgent, input engine.InspectInput) (string, error) {
	switch input.Mode {
	case "health":
		return agent.GetHealth(ctx), nil

	case modeStatus:
		if input.Service == "" {
			// LLM sometimes omits the required service parameter — fall back to health mode.
			return agent.GetHealth(ctx), nil
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

	case modeLogs:
		return handleInspectLogs(ctx, agent, input)

	case "analyze":
		return handleInspectAnalyze(ctx, agent, input)

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

	case "groups":
		groups := agent.GetServiceGroups(ctx)
		if len(groups) == 0 || (len(groups) == 1 && groups[0].Name == "") {
			return "No service groups configured. Set dozor.group labels on containers.", nil
		}
		return engine.FormatGroups(groups), nil

	default:
		return "", fmt.Errorf("unknown mode %q, use: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd, connections, cron, groups", input.Mode)
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
		return "", errors.New("command is required")
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
		return "", errors.New("command is required")
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
		return "Restart failed: " + result.Output(), nil
	}
	if result.Stdout != "" {
		return result.Stdout, nil
	}
	return fmt.Sprintf("Service %s restarted successfully.", service), nil
}

// HandleDeploy processes a server_deploy request.
func HandleDeploy(ctx context.Context, agent *engine.ServerAgent, input engine.DeployInput) (string, error) {
	if input.Action == "health" {
		return agent.CheckDeployHealth(ctx, input.Services), nil
	}
	if input.DeployID != "" || input.Action == modeStatus {
		return handleDeployStatus(ctx, agent, input)
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

// handleDeployStatus returns the status of an existing deploy.
func handleDeployStatus(ctx context.Context, agent *engine.ServerAgent, input engine.DeployInput) (string, error) {
	if input.DeployID == "" {
		return "", errors.New("deploy_id is required for status action")
	}
	if ok, reason := engine.ValidateDeployID(input.DeployID); !ok {
		return "", fmt.Errorf("invalid deploy ID: %s", reason)
	}
	status := agent.GetDeployStatus(ctx, input.DeployID)
	text := fmt.Sprintf("Deploy: %s\nStatus: %s\nProcess running: %v\nLog file: %s\n",
		input.DeployID, status.Status, status.ProcessRunning, status.LogFile)
	if status.LogContent != "" {
		lines := status.LogContent
		if len(lines) > maxDeployLogChars {
			lines = "...\n" + lines[len(lines)-maxDeployLogChars:]
		}
		text += "\nLog output:\n" + lines
	}
	return text, nil
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
		return "", errors.New("no user services found (auto-discovery found none, or set DOZOR_USER_SERVICES in .env)")
	}

	switch input.Action {
	case modeStatus:
		return userServicesStatus(ctx, agent, input.Service, services), nil
	case "restart":
		if input.Service == "" {
			return "", errors.New("service name is required for restart action")
		}
		if engine.FindUserServiceIn(services, input.Service) == nil {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.UserServiceNamesFrom(services), ", "))
		}
		return userServiceRestart(ctx, agent, input.Service), nil
	case "restart-all":
		return userServicesRestartAll(ctx, agent, services), nil
	case modeLogs:
		if input.Service == "" {
			return "", errors.New("service name is required for logs action")
		}
		if engine.FindUserServiceIn(services, input.Service) == nil {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.UserServiceNamesFrom(services), ", "))
		}
		lines := input.Lines
		if lines <= 0 {
			lines = 50
		}
		if lines > maxLinesServices {
			lines = maxLinesServices
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
			return "", errors.New("binary name is required for install action")
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
		return "", errors.New("no remote services configured (set DOZOR_REMOTE_HOST and DOZOR_REMOTE_SERVICES in .env)")
	}

	switch input.Action {
	case modeStatus:
		return engine.RemoteServiceStatus(ctx, cfg), nil
	case "restart":
		if input.Service == "" {
			return "", errors.New("service name is required for restart action")
		}
		if !engine.IsValidRemoteService(cfg, input.Service) {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
		}
		return engine.RemoteRestart(ctx, cfg, input.Service), nil
	case modeLogs:
		if input.Service == "" {
			return "", errors.New("service name is required for logs action")
		}
		if !engine.IsValidRemoteService(cfg, input.Service) {
			return "", fmt.Errorf("unknown service %q, available: %s", input.Service, strings.Join(engine.RemoteServiceNames(cfg), ", "))
		}
		lines := input.Lines
		if lines <= 0 {
			lines = 50
		}
		if lines > maxLinesServices {
			lines = maxLinesServices
		}
		return engine.RemoteLogs(ctx, cfg, input.Service, lines), nil
	default:
		return "", fmt.Errorf("unknown action %q, use: status, restart, logs", input.Action)
	}
}

// handleInspectLogs handles the "logs" mode for HandleInspect.
func handleInspectLogs(ctx context.Context, agent *engine.ServerAgent, input engine.InspectInput) (string, error) {
	if input.Service == "" {
		return "", errors.New("service is required for logs mode")
	}
	if ok, reason := engine.ValidateServiceName(input.Service); !ok {
		return "", fmt.Errorf("invalid service: %s", reason)
	}
	lines := input.Lines
	if lines <= 0 {
		lines = 100
	}
	if lines > maxLinesLog {
		return "", fmt.Errorf("lines must be <= %d", maxLinesLog)
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
}

// handleInspectAnalyze handles the "analyze" mode for HandleInspect.
func handleInspectAnalyze(ctx context.Context, agent *engine.ServerAgent, input engine.InspectInput) (string, error) {
	if input.Service == "" {
		return agent.AnalyzeAll(ctx), nil
	}
	if ok, reason := engine.ValidateServiceName(input.Service); !ok {
		return "", fmt.Errorf("invalid service: %s", reason)
	}
	entries := agent.GetLogs(ctx, input.Service, 1000, false)
	status := agent.GetServiceStatus(ctx, input.Service)
	var extra []engine.ErrorPattern
	if p := status.DozorLabel("logs.pattern"); p != "" {
		extra = append(extra, engine.LabelPattern(p))
	}
	result := engine.AnalyzeLogs(entries, input.Service, extra...)
	return engine.FormatAnalysisEnriched(result, entries), nil
}
