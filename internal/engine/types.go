package engine

import "time"

// AlertLevel severity levels.
type AlertLevel string

const (
	AlertCritical AlertLevel = "critical"
	AlertError    AlertLevel = "error"
	AlertWarning  AlertLevel = "warning"
	AlertInfo     AlertLevel = "info"
)

// ContainerState represents docker container states.
type ContainerState string

const (
	StateRunning    ContainerState = "running"
	StateExited     ContainerState = "exited"
	StateRestarting ContainerState = "restarting"
	StatePaused     ContainerState = "paused"
	StateDead       ContainerState = "dead"
	StateUnknown    ContainerState = "unknown"
)

// CommandResult from local or SSH execution.
type CommandResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ReturnCode int    `json:"return_code"`
	Command    string `json:"command"`
	Success    bool   `json:"success"`
}

// Output returns stdout if non-empty, else stderr.
func (c CommandResult) Output() string {
	if c.Stdout != "" {
		return c.Stdout
	}
	return c.Stderr
}

// LogEntry parsed from container logs.
type LogEntry struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Level     string     `json:"level"`
	Message   string     `json:"message"`
	Service   string     `json:"service"`
	Raw       string     `json:"raw"`
}

// ServiceStatus for a docker container.
type ServiceStatus struct {
	Name          string         `json:"name"`
	State         ContainerState `json:"state"`
	Health        string         `json:"health,omitempty"`
	Uptime        string         `json:"uptime,omitempty"`
	RestartCount  int            `json:"restart_count"`
	CPUPercent    *float64       `json:"cpu_percent,omitempty"`
	MemoryMB      *float64       `json:"memory_mb,omitempty"`
	MemoryLimitMB *float64       `json:"memory_limit_mb,omitempty"`
	ErrorCount    int            `json:"error_count"`
	RecentErrors  []LogEntry     `json:"recent_errors,omitempty"`
}

// IsHealthy returns true if the service is running with no restarts or errors.
func (s ServiceStatus) IsHealthy() bool {
	return s.State == StateRunning && s.RestartCount == 0 && s.ErrorCount == 0
}

// GetAlertLevel returns the alert level based on service state.
func (s ServiceStatus) GetAlertLevel() AlertLevel {
	if s.State != StateRunning {
		return AlertCritical
	}
	if s.RestartCount > 0 || s.ErrorCount > 5 {
		return AlertError
	}
	if s.ErrorCount > 0 {
		return AlertWarning
	}
	return AlertInfo
}

// Alert represents a monitoring alert.
type Alert struct {
	Level           AlertLevel `json:"level"`
	Service         string     `json:"service"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	SuggestedAction string     `json:"suggested_action"`
	Timestamp       time.Time  `json:"timestamp"`
}

// SecurityIssue from security audit.
type SecurityIssue struct {
	Level       AlertLevel `json:"level"`
	Category    string     `json:"category"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Remediation string     `json:"remediation"`
	Evidence    string     `json:"evidence,omitempty"`
}

// DiagnosticReport is the full server diagnostic.
type DiagnosticReport struct {
	Timestamp     time.Time       `json:"timestamp"`
	Server        string          `json:"server"`
	Services      []ServiceStatus `json:"services"`
	Alerts        []Alert         `json:"alerts"`
	OverallHealth string          `json:"overall_health"`
}

// CalculateHealth sets OverallHealth based on services and alerts.
func (r *DiagnosticReport) CalculateHealth() {
	for _, s := range r.Services {
		if s.State != StateRunning {
			r.OverallHealth = "critical"
			return
		}
	}
	for _, a := range r.Alerts {
		if a.Level == AlertCritical {
			r.OverallHealth = "critical"
			return
		}
	}
	for _, a := range r.Alerts {
		if a.Level == AlertError {
			r.OverallHealth = "degraded"
			return
		}
	}
	for _, a := range r.Alerts {
		if a.Level == AlertWarning {
			r.OverallHealth = "warning"
			return
		}
	}
	r.OverallHealth = "healthy"
}

// NeedsAttention returns true if health is critical or degraded.
func (r DiagnosticReport) NeedsAttention() bool {
	return r.OverallHealth == "critical" || r.OverallHealth == "degraded"
}

// DeployResult from starting a deploy.
type DeployResult struct {
	Success  bool   `json:"success"`
	DeployID string `json:"deploy_id,omitempty"`
	LogFile  string `json:"log_file,omitempty"`
	Error    string `json:"error,omitempty"`
}

// DeployStatus from checking deploy progress.
type DeployStatus struct {
	Status         string `json:"status"` // RUNNING, COMPLETED, FAILED, UNKNOWN
	ProcessRunning bool   `json:"process_running"`
	LogFile        string `json:"log_file"`
	LogContent     string `json:"log_content"`
}

// ErrorPattern for log analysis.
type ErrorPattern struct {
	Pattern         string
	Level           AlertLevel
	Category        string
	Description     string
	SuggestedAction string
	Services        []string // nil = all services
}

// RemoteServerStatus for remote host monitoring.
type RemoteServerStatus struct {
	Host       string            `json:"host"`
	HTTPStatus int               `json:"http_status,omitempty"`
	SSLExpiry  *time.Time        `json:"ssl_expiry,omitempty"`
	Services   map[string]string `json:"services,omitempty"` // name -> "active"/"inactive"
	DiskUsage  string            `json:"disk_usage,omitempty"`
	LoadAvg    string            `json:"load_avg,omitempty"`
	Alerts     []Alert           `json:"alerts,omitempty"`
}

// TextOutput is a generic text output for MCP tools.
type TextOutput struct {
	Text string `json:"text"`
}

// --- MCP tool input structs ---

type InspectInput struct {
	Mode     string   `json:"mode" jsonschema:"Inspection mode: health, status, diagnose, logs, analyze, security, overview, remote, systemd"`
	Service  string   `json:"service,omitempty" jsonschema:"Service name (required for status, logs, analyze modes)"`
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

// CleanupTarget represents a single cleanup target result.
type CleanupTarget struct {
	Name      string  `json:"name"`
	Available bool    `json:"available"`
	SizeMB    float64 `json:"size_mb"`
	Freed     string  `json:"freed,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// DiskPressure represents disk usage for a filesystem.
type DiskPressure struct {
	Filesystem string  `json:"filesystem"`
	UsedPct    float64 `json:"used_pct"`
	AvailGB    float64 `json:"avail_gb"`
	MountPoint string  `json:"mount_point"`
}
