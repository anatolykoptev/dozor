package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/dozor/internal/deploy"
)

// manualDeployStatus tracks in-flight SHA-pinned deploys started by
// StartManualDeploy. Unlike the legacy StartDeploy (nohup + pgrep), a manual
// deploy runs as a goroutine inside dozor, so neither pgrep nor the log file
// can answer "is it still running" — the registry can (issue #201).
const (
	manualDeployRunning   = "RUNNING"
	manualDeployCompleted = "COMPLETED"
	manualDeployFailed    = "FAILED"
)

var (
	manualDeployMu     sync.Mutex
	manualDeployStates = map[string]string{} // deployID → manualDeploy* value
)

func setManualDeployStatus(id, status string) {
	manualDeployMu.Lock()
	// Unbounded growth guard: entries are ~40B, but the map would otherwise
	// grow one per manual deploy for the process lifetime.
	if len(manualDeployStates) >= 64 {
		for k := range manualDeployStates {
			delete(manualDeployStates, k)
			break
		}
	}
	manualDeployStates[id] = status
	manualDeployMu.Unlock()
}

func getManualDeployStatus(id string) (string, bool) {
	manualDeployMu.Lock()
	defer manualDeployMu.Unlock()
	s, ok := manualDeployStates[id]
	return s, ok
}

// StartManualDeploy triggers a SHA-pinned deploy for a repo configured in
// deploy-repos.yaml, using the same git-worktree pipeline as the webhook path.
//
// It launches deploy.ExecuteManualDeploy in a background goroutine, writing
// output to a temp log file, and returns a deployID that the caller can poll
// with GetDeployStatus.
//
// When req.FromDisk is true, the SHA-pinning is skipped and the on-disk
// working tree is built as-is (debug opt-out — always logs a WARN).
func (a *ServerAgent) StartManualDeploy(ctx context.Context, req deploy.ManualDeployRequest) DeployResult {
	deployID := fmt.Sprintf("deploy-manual-%d", time.Now().Unix())
	logFile := fmt.Sprintf("/tmp/%s.log", deployID)

	// Open log file before launching the goroutine so we can report errors
	// immediately rather than silently losing them if the open fails.
	f, err := os.Create(logFile) //nolint:gosec // path is under /tmp with a time-based name
	if err != nil {
		return DeployResult{
			Success: false,
			Error:   fmt.Sprintf("create deploy log: %v", err),
		}
	}
	f.Close() //nolint:errcheck // just ensuring file exists before goroutine writes

	// Register the deploy BEFORE the goroutine so GetDeployStatus answers
	// truthfully from the first instant (issue #201: the file stayed empty
	// for the whole build and pgrep -f can't see a goroutine, so the status
	// endpoint reported UNKNOWN for an ID it had just advertised).
	setManualDeployStatus(deployID, manualDeployRunning)
	// Same for the log file: a RUNNING marker keeps the advertised path
	// honest while the real build output continues to journald.
	_ = os.WriteFile(logFile, []byte(fmt.Sprintf("RUNNING: %s\n", deployID)), 0o600) //nolint:mnd

	go func() {
		// Use a fresh background context so the deploy survives the MCP
		// request context being cancelled (MCP requests are short-lived).
		bctx := context.Background()

		slog.Info("deploy/manual: starting background deploy",
			"repo", req.Repo,
			"services", req.Config.Services,
			"branch", req.Config.Branch,
			"from_disk", req.FromDisk,
			"deploy_id", deployID,
			"log_file", logFile,
		)

		result := deploy.ExecuteManualDeploy(bctx, req)

		var line string
		if result.Success {
			deploy.RecordManualDeployReceipt(req, result.BuiltSHA)
			setManualDeployStatus(deployID, manualDeployCompleted)
			line = fmt.Sprintf("DEPLOY COMPLETE: %s (sha=%s)\n", deployID, result.BuiltSHA)
		} else {
			setManualDeployStatus(deployID, manualDeployFailed)
			line = fmt.Sprintf("DEPLOY FAILED: %s: %s\n", deployID, result.Error)
		}

		if werr := os.WriteFile(logFile, []byte(line), 0o600); werr != nil { //nolint:mnd
			slog.Warn("deploy/manual: failed to write outcome to log file",
				"deploy_id", deployID,
				"log_file", logFile,
				"error", werr,
			)
		}
	}()

	return DeployResult{
		Success:  true,
		DeployID: deployID,
		LogFile:  logFile,
	}
}

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
	time.Sleep(30 * time.Second)
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

	// Check if process is still running. For deploys started by
	// StartManualDeploy the "process" is a goroutine INSIDE dozor — pgrep
	// can never see it — so consult the registry first (issue #201).
	manualStatus, isManual := getManualDeployStatus(deployID)
	processRunning := manualStatus == manualDeployRunning
	if !isManual {
		pRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("pgrep -f %s 2>/dev/null", deployID))
		processRunning = pRes.Success && strings.TrimSpace(pRes.Stdout) != ""
	}

	// Read log file
	lRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("cat %s 2>/dev/null", logFile))
	logContent := lRes.Stdout

	var status string
	switch {
	case isManual:
		status = manualStatus
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
