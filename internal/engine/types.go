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

// IsErrorLevel returns true if the log entry is ERROR, FATAL, or CRITICAL.
func (e LogEntry) IsErrorLevel() bool {
	return e.Level == "ERROR" || e.Level == "FATAL" || e.Level == "CRITICAL"
}

// ServiceStatus for a docker container.
type ServiceStatus struct {
	Name          string            `json:"name"`
	State         ContainerState    `json:"state"`
	Health        string            `json:"health,omitempty"`
	Uptime        string            `json:"uptime,omitempty"`
	RestartCount  int               `json:"restart_count"`
	CPUPercent    *float64          `json:"cpu_percent,omitempty"`
	MemoryMB      *float64          `json:"memory_mb,omitempty"`
	MemoryLimitMB *float64          `json:"memory_limit_mb,omitempty"`
	ErrorCount    int               `json:"error_count"`
	RecentErrors  []LogEntry        `json:"recent_errors,omitempty"`
	Labels        map[string]string `json:"-"`

	HealthcheckURL string `json:"healthcheck_url,omitempty"`
	HealthcheckOK  *bool  `json:"healthcheck_ok,omitempty"`
	HealthcheckMsg string `json:"healthcheck_msg,omitempty"`
	AlertChannel   string `json:"alert_channel,omitempty"`
}

// DozorLabel returns a dozor-specific label value.
func (s ServiceStatus) DozorLabel(key string) string {
	return s.Labels["dozor."+key]
}

// IsHealthy returns true if the service is running with no restarts or errors.
func (s ServiceStatus) IsHealthy() bool {
	if s.HealthcheckOK != nil && !*s.HealthcheckOK {
		return false
	}
	return s.State == StateRunning && s.RestartCount == 0 && s.ErrorCount == 0
}

// GetAlertLevel returns the alert level based on service state.
func (s ServiceStatus) GetAlertLevel() AlertLevel {
	if s.State != StateRunning {
		return AlertCritical
	}
	if s.HealthcheckOK != nil && !*s.HealthcheckOK {
		return AlertError
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
	Channel         string     `json:"channel,omitempty"`
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

// TrackedBinaryConfig is a user-configured binary to track (owner/repo:binary).
type TrackedBinaryConfig struct {
	Owner  string
	Repo   string
	Binary string
}

// TrackedBinary represents a binary with version info from GitHub.
type TrackedBinary struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Owner          string `json:"owner"`
	Repo           string `json:"repo"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version,omitempty"`
	Status         string `json:"status"` // UPDATE, OK, SKIP, ERROR
	ReleaseURL     string `json:"release_url,omitempty"`
	Error          string `json:"error,omitempty"`
}

// CleanupTarget represents a single cleanup target result.
type CleanupTarget struct {
	Name      string  `json:"name"`
	Available bool    `json:"available"`
	SizeMB    float64 `json:"size_mb"`
	Freed     string  `json:"freed,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// ErrorCluster groups similar error messages by normalized template.
type ErrorCluster struct {
	Template string `json:"template"`
	Count    int    `json:"count"`
	Example  string `json:"example"`
}

// DiskPressure represents disk usage for a filesystem.
type DiskPressure struct {
	Filesystem string  `json:"filesystem"`
	UsedPct    float64 `json:"used_pct"`
	AvailGB    float64 `json:"avail_gb"`
	MountPoint string  `json:"mount_point"`
}

// UserService represents a user-level systemd service with optional port.
type UserService struct {
	Name string `json:"name"`
	Port int    `json:"port,omitempty"` // 0 if not specified
}

// ServiceGroup holds services organized by dozor.group label.
type ServiceGroup struct {
	Name     string          `json:"name"`
	Services []ServiceStatus `json:"services"`
	Health   string          `json:"health"` // worst of members: critical > degraded > warning > healthy
}

// DependencyGraph maps service names to their dependencies (dozor.depends_on).
type DependencyGraph map[string][]string

// Dependents returns all services that transitively depend on the given service.
// Uses BFS with visited set for cycle safety. Returns in dependency-first order
// (if A depends on X and B depends on A, returns [A, B]).
func (g DependencyGraph) Dependents(service string) []string {
	// Build reverse map: dependency -> services that depend on it
	reverse := make(map[string][]string)
	for svc, deps := range g {
		for _, dep := range deps {
			reverse[dep] = append(reverse[dep], svc)
		}
	}

	// BFS from the target service
	var result []string
	visited := map[string]bool{service: true}
	queue := []string{service}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, dependent := range reverse[current] {
			if visited[dependent] {
				continue
			}
			visited[dependent] = true
			result = append(result, dependent)
			queue = append(queue, dependent)
		}
	}

	return result
}
