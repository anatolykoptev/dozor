package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const maintenanceLockPath = "/tmp/krolik-server-maintenance.lock"

// waitForMaintenanceLock blocks until the lock file is removed or deadline expires.
// Returns nil immediately if no lock file exists.
func waitForMaintenanceLock(ctx context.Context, services []string) error {
	if _, err := os.Stat(maintenanceLockPath); err != nil {
		return nil // no lock
	}

	// Read lock metadata: "who: reason" format (optional)
	lockInfo := "unknown"
	if data, err := os.ReadFile(maintenanceLockPath); err == nil {
		if content := strings.TrimSpace(string(data)); content != "" {
			lockInfo = content
		}
	}

	slog.Info("deploy: maintenance lock detected, waiting",
		"lock", maintenanceLockPath,
		"locked_by", lockInfo,
		"services", services,
	)
	deadline := time.After(maintenanceMaxWait)
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for maintenance lock (locked by: %s)", lockInfo)
		case <-deadline:
			return fmt.Errorf("maintenance lock %s not released after %s (locked by: %s)",
				maintenanceLockPath, maintenanceMaxWait, lockInfo)
		case <-time.After(maintenancePollInterval):
			if _, err := os.Stat(maintenanceLockPath); err != nil {
				slog.Info("deploy: maintenance lock released, proceeding")
				return nil
			}
		}
	}
}

func (q *Queue) executeBuild(ctx context.Context, req BuildRequest) BuildResult {
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.Services,
	}

	// Step 0: wait for maintenance lock to clear
	if err := waitForMaintenanceLock(ctx, req.Config.Services); err != nil {
		result.Error = err.Error()
		return result
	}

	if errMsg := gitPull(ctx, req.Config.SourcePath, req.CommitSHA); errMsg != "" {
		result.Error = errMsg
		return result
	}

	if errMsg := composeBuild(ctx, req); errMsg != "" {
		result.Error = errMsg
		return result
	}

	result.PreviousImages = snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)

	if errMsg := composeUp(ctx, req); errMsg != "" {
		result.Error = errMsg
		q.tryRollback(ctx, &result, req.Config.ComposePath)
		return result
	}

	// Step 4: health check (brief wait + verify running + port mapping)
	time.Sleep(healthWait)
	for _, svc := range req.Config.Services {
		if err := checkHealth(ctx, req.Config.ComposePath, svc); err != nil {
			if strings.Contains(err.Error(), "port mapping") {
				slog.Warn("deploy: port mapping lost, force-recreating",
					"service", svc,
					"error", err,
				)
				// One targeted force-recreate attempt
				recreateArgs := []string{"compose", "up", "-d", "--no-deps", "--force-recreate", svc}
				if rerr := runCmd(ctx, req.Config.ComposePath, "docker", recreateArgs...); rerr != nil {
					result.Error = fmt.Sprintf("port recovery %s: %v (original: %v)", svc, rerr, err)
					q.tryRollback(ctx, &result, req.Config.ComposePath)
					return result
				}
				time.Sleep(portRecoveryWait)
				if err2 := checkHealth(ctx, req.Config.ComposePath, svc); err2 != nil {
					result.Error = fmt.Sprintf("health check %s after port recovery: %v", svc, err2)
					q.tryRollback(ctx, &result, req.Config.ComposePath)
					return result
				}
				continue // recovery succeeded
			}
			result.Error = fmt.Sprintf("health check %s: %v", svc, err)
			q.tryRollback(ctx, &result, req.Config.ComposePath)
			return result
		}
	}

	if err := smokeTest(ctx, req.Config.SmokeURL); err != nil {
		result.Error = fmt.Sprintf("smoke test: %v", err)
		q.tryRollback(ctx, &result, req.Config.ComposePath)
		return result
	}

	// Step 6: cleanup dangling images and old build cache (best-effort)
	pruneOldImages(ctx, req.Config.ComposePath)

	result.Success = true
	return result
}

// tryRollback attempts to restore services to previous images on deploy failure.
func (q *Queue) tryRollback(ctx context.Context, result *BuildResult, composePath string) {
	if len(result.PreviousImages) == 0 {
		return
	}
	if err := rollbackImages(ctx, composePath, result.Services, result.PreviousImages); err != nil {
		result.Error += fmt.Sprintf(" | rollback also failed: %v", err)
		return
	}
	result.RolledBack = true
	result.Error += " | rolled back to previous version"
}
