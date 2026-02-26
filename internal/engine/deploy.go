package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StartDeploy begins a background deploy via nohup.
func (a *ServerAgent) StartDeploy(ctx context.Context, projectPath string, services []string, build, pull bool) DeployResult {
	path := projectPath
	if path == "" {
		path = a.cfg.ComposePath
	}
	if strings.HasPrefix(path, "~") {
		path = "$HOME" + path[1:]
	}

	deployID := fmt.Sprintf("deploy-%d", time.Now().Unix())
	logFile := fmt.Sprintf("${TMPDIR:-/tmp}/%s.log", deployID)

	// Build the deploy command
	var parts []string
	parts = append(parts, "cd "+path)

	if pull {
		parts = append(parts, "docker compose pull")
	}

	composeUp := "docker compose up -d"
	if build {
		composeUp += " --build"
	}
	if len(services) > 0 {
		composeUp += " " + strings.Join(services, " ")
	}
	parts = append(parts, composeUp)
	parts = append(parts, fmt.Sprintf("echo 'DEPLOY COMPLETE: %s'", deployID))

	script := strings.Join(parts, " && ")
	cmd := fmt.Sprintf("nohup bash -c '%s' > %s 2>&1 &", script, logFile)

	res := a.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success {
		return DeployResult{
			Success: false,
			Error:   "Failed to start deploy: " + res.Stderr,
		}
	}

	return DeployResult{
		Success:  true,
		DeployID: deployID,
		LogFile:  logFile,
	}
}

// CheckDeployHealth verifies all services are running after a deploy.
// Returns a summary string with OK/FAIL per service.
func (a *ServerAgent) CheckDeployHealth(ctx context.Context, services []string) string {
	time.Sleep(10 * time.Second)
	services = a.resolveServices(ctx, services)
	if len(services) == 0 {
		return "Post-deploy check: no services found."
	}

	statuses := a.status.GetAllStatuses(ctx, services)
	var b strings.Builder
	b.WriteString("Post-deploy health:\n")
	allOK := true
	for _, s := range statuses {
		icon := "OK"
		if s.State != StateRunning {
			icon = "FAIL"
			allOK = false
		}
		fmt.Fprintf(&b, "  [%s] %s (%s)\n", icon, s.Name, s.State)
	}
	if allOK {
		b.WriteString("All services running after deploy.")
	} else {
		b.WriteString("WARNING: some services did not start. Check logs.")
	}
	return b.String()
}

// GetDeployStatus checks a running deploy.
func (a *ServerAgent) GetDeployStatus(ctx context.Context, deployID string) DeployStatus {
	logFile := fmt.Sprintf("${TMPDIR:-/tmp}/%s.log", deployID)

	// Check if process is still running
	pRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("pgrep -f %s 2>/dev/null", deployID))
	processRunning := pRes.Success && strings.TrimSpace(pRes.Stdout) != ""

	// Read log file
	lRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("cat %s 2>/dev/null", logFile))
	logContent := lRes.Stdout

	var status string
	switch {
	case processRunning:
		status = "RUNNING"
	case strings.Contains(logContent, "DEPLOY COMPLETE"):
		status = "COMPLETED"
	case logContent != "":
		status = "FAILED"
	default:
		status = "UNKNOWN"
	}

	return DeployStatus{
		Status:         status,
		ProcessRunning: processRunning,
		LogFile:        logFile,
		LogContent:     logContent,
	}
}
