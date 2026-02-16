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
	logFile := fmt.Sprintf("/tmp/%s.log", deployID)

	// Build the deploy command
	var parts []string
	parts = append(parts, fmt.Sprintf("cd %s", path))

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

// GetDeployStatus checks a running deploy.
func (a *ServerAgent) GetDeployStatus(ctx context.Context, deployID string) DeployStatus {
	logFile := fmt.Sprintf("/tmp/%s.log", deployID)

	// Check if process is still running
	pRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("pgrep -f %s 2>/dev/null", deployID))
	processRunning := pRes.Success && strings.TrimSpace(pRes.Stdout) != ""

	// Read log file
	lRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("cat %s 2>/dev/null", logFile))
	logContent := lRes.Stdout

	status := "UNKNOWN"
	if processRunning {
		status = "RUNNING"
	} else if strings.Contains(logContent, "DEPLOY COMPLETE") {
		status = "COMPLETED"
	} else if logContent != "" {
		status = "FAILED"
	}

	return DeployStatus{
		Status:         status,
		ProcessRunning: processRunning,
		LogFile:        logFile,
		LogContent:     logContent,
	}
}
