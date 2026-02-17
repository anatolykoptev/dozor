package engine

// --- MCP tool input structs ---

type InspectInput struct {
	Mode     string   `json:"mode" jsonschema:"Inspection mode: health, status, diagnose, logs, analyze, errors, security, overview, remote, systemd"`
	Service  string   `json:"service,omitempty" jsonschema:"Service name (required for status, logs modes; optional for analyze)"`
	Services []string `json:"services,omitempty" jsonschema:"Services to diagnose (all if omitted, only for diagnose mode)"`
	Lines    int      `json:"lines,omitempty" jsonschema:"Number of log lines (default 100, only for logs mode)"`
}

type ExecInput struct {
	Command string `json:"command" jsonschema:"Shell command to execute"`
}

type RestartInput struct {
	Service string `json:"service" jsonschema:"Service to restart"`
}

type PruneInput struct {
	Images     *bool  `json:"images,omitempty" jsonschema:"Prune unused images (default true)"`
	BuildCache *bool  `json:"build_cache,omitempty" jsonschema:"Prune build cache (default true)"`
	Volumes    *bool  `json:"volumes,omitempty" jsonschema:"Prune unused volumes (default false)"`
	Age        string `json:"age,omitempty" jsonschema:"Prune items older than (default 24h)"`
}

type DeployInput struct {
	DeployID    string   `json:"deploy_id,omitempty" jsonschema:"Deploy ID to check status (if provided, checks existing deploy instead of starting new one)"`
	ProjectPath string   `json:"project_path,omitempty" jsonschema:"Path to docker-compose project"`
	Services    []string `json:"services,omitempty" jsonschema:"Specific services to deploy"`
	Build       *bool    `json:"build,omitempty" jsonschema:"Build images before deploy (default true)"`
	Pull        *bool    `json:"pull,omitempty" jsonschema:"Pull images before deploy (default true)"`
}

type CleanupInput struct {
	Targets []string `json:"targets,omitempty" jsonschema:"Cleanup targets: docker, go, npm, uv, pip, journal, tmp, caches, all"`
	Report  *bool    `json:"report,omitempty" jsonschema:"Dry-run: scan sizes without deleting (default true)"`
	MinAge  string   `json:"min_age,omitempty" jsonschema:"Minimum age for cleanup (e.g. 7d, 24h, 3d)"`
}

type TriageInput struct {
	Services []string `json:"services,omitempty" jsonschema:"Specific services to triage (all if omitted)"`
}

// RemoteExecInput is the MCP tool input for server_remote_exec.
type RemoteExecInput struct {
	Command string `json:"command" jsonschema:"Shell command to execute on the remote server via SSH"`
}

// ServicesInput is the MCP tool input for server_services.
type ServicesInput struct {
	Action  string `json:"action" jsonschema:"Action: status, restart, logs, restart-all"`
	Service string `json:"service,omitempty" jsonschema:"Service name (required for restart, logs)"`
	Lines   int    `json:"lines,omitempty" jsonschema:"Number of log lines (default 50, for logs action)"`
}

// UpdatesInput is the MCP tool input for server_updates.
type UpdatesInput struct {
	Action string `json:"action" jsonschema:"Action: check (scan binaries for updates), install (download and replace binary)"`
	Binary string `json:"binary,omitempty" jsonschema:"Binary name to install update for (required for install action)"`
}

// RemoteInput is the MCP tool input for server_remote.
type RemoteInput struct {
	Action  string `json:"action" jsonschema:"Action: status (show all remote services), restart (restart a service), logs (view service logs)"`
	Service string `json:"service,omitempty" jsonschema:"Service name (required for restart and logs actions)"`
	Lines   int    `json:"lines,omitempty" jsonschema:"Number of log lines (default 50, max 5000, for logs action)"`
}
