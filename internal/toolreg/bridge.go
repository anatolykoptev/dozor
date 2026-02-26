package toolreg

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/tools"
)

const (
	// defaultLogLines is the default number of log lines for bridge tools.
	defaultLogLines = 50
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
}

// helpers for parsing map[string]any args into typed values

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

func getBoolPtr(args map[string]any, key string) *bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return &b
		}
	}
	return nil
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
	return `Inspect server state. Modes: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd, connections, cron`
}
func (t *inspectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode":     map[string]any{"type": "string", "description": "Inspection mode: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd, connections, cron"},
			"service":  map[string]any{"type": "string", "description": "Service name (required for status, logs modes; optional for analyze)"},
			"services": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Services to diagnose (all if omitted)"},
			"lines":    map[string]any{"type": "integer", "description": "Number of log lines (default 100, only for logs mode)"},
			"filter":   map[string]any{"type": "string", "description": "Filter log lines by substring (case-insensitive)"},
		},
		"required": []string{"mode"},
	}
}
func (t *inspectTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return tools.HandleInspect(ctx, t.agent, engine.InspectInput{
		Mode:     getString(args, "mode"),
		Service:  getString(args, "service"),
		Services: getStringSlice(args, "services"),
		Lines:    getInt(args, "lines", 100),
		Filter:   getString(args, "filter"),
	})
}

// --- server_triage ---

type triageTool struct{ agent *engine.ServerAgent }

func (t *triageTool) Name() string { return "server_triage" }
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
	return tools.HandleTriage(ctx, t.agent, engine.TriageInput{
		Services: getStringSlice(args, "services"),
	})
}

// --- server_exec ---

type execTool struct{ agent *engine.ServerAgent }

func (t *execTool) Name() string { return "server_exec" }
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
	return tools.HandleExecSafe(ctx, t.agent, getString(args, "command"))
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
	return tools.HandleRemoteExec(ctx, t.agent, getString(args, "command"))
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
	return tools.HandleRestart(ctx, t.agent, getString(args, "service"))
}

// --- server_deploy ---

type deployTool struct{ agent *engine.ServerAgent }

func (t *deployTool) Name() string { return "server_deploy" }
func (t *deployTool) Description() string {
	return "Deploy or check deploy status. If deploy_id is provided, checks existing deploy status. Otherwise starts a new background deployment (pulls, builds, docker compose up)."
}
func (t *deployTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":       map[string]any{"type": "string", "description": "Action: deploy (default), status, health"},
			"deploy_id":    map[string]any{"type": "string", "description": "Deploy ID to check status"},
			"project_path": map[string]any{"type": "string", "description": "Path to docker-compose project"},
			"services":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Specific services to deploy"},
			"build":        map[string]any{"type": "boolean", "description": "Build images before deploy (default true)"},
			"pull":         map[string]any{"type": "boolean", "description": "Pull images before deploy (default true)"},
		},
	}
}
func (t *deployTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return tools.HandleDeploy(ctx, t.agent, engine.DeployInput{
		Action:      getString(args, "action"),
		DeployID:    getString(args, "deploy_id"),
		ProjectPath: getString(args, "project_path"),
		Services:    getStringSlice(args, "services"),
		Build:       getBoolPtr(args, "build"),
		Pull:        getBoolPtr(args, "pull"),
	})
}

// --- server_prune ---

type pruneTool struct{ agent *engine.ServerAgent }

func (t *pruneTool) Name() string { return "server_prune" }
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
	return tools.HandlePrune(ctx, t.agent, engine.PruneInput{
		Images:     getBoolPtr(args, "images"),
		BuildCache: getBoolPtr(args, "build_cache"),
		Volumes:    getBoolPtr(args, "volumes"),
		Age:        getString(args, "age"),
	})
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
	return tools.HandleCleanup(ctx, t.agent, engine.CleanupInput{
		Targets: getStringSlice(args, "targets"),
		Report:  getBoolPtr(args, "report"),
		MinAge:  getString(args, "min_age"),
	})
}

// --- server_services ---

type servicesTool struct{ agent *engine.ServerAgent }

func (t *servicesTool) Name() string { return "server_services" }
func (t *servicesTool) Description() string {
	return `Manage user-level systemd services. Actions: status, restart, logs, restart-all`
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
	return tools.HandleServices(ctx, t.agent, engine.ServicesInput{
		Action:  getString(args, "action"),
		Service: getString(args, "service"),
		Lines:   getInt(args, "lines", defaultLogLines),
	})
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
	return tools.HandleUpdates(ctx, t.agent, engine.UpdatesInput{
		Action: getString(args, "action"),
		Binary: getString(args, "binary"),
	})
}

// --- server_remote ---

type remoteTool struct{ agent *engine.ServerAgent }

func (t *remoteTool) Name() string { return "server_remote" }
func (t *remoteTool) Description() string {
	return `Manage remote server services (system-level systemd via sudo). Actions: status, restart, logs`
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
	return tools.HandleRemote(ctx, t.agent, engine.RemoteInput{
		Action:  getString(args, "action"),
		Service: getString(args, "service"),
		Lines:   getInt(args, "lines", defaultLogLines),
	})
}
