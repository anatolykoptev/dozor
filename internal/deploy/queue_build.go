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
	ctx, cancel := context.WithTimeout(ctx, req.Config.BuildTimeout.OrDefault(buildTimeout))
	defer cancel()

	// deploy_on: manual gate — production must never deploy automatically
	// (issue #183). The gate sits in executeBuild, the single chokepoint ALL
	// automatic deploy entry points funnel through (webhook release, webhook
	// push, debounce/requeue recovery → queue.Submit → processBuild → here).
	// The explicit human path (ExecuteManualDeploy / server_deploy) does NOT
	// call executeBuild, so it is never gated — that is the whole point.
	//
	// For compose: the build/pull + image-cache publish still run (build-once-
	// promote lets a manual prod entry reuse the canary's published tree-hash
	// image), then composeUp is withheld and the pending-deploy gauge is set
	// so a released-but-never-deployed fix is visible. For binary/static the
	// build IS the deploy (restart / deploy script) and is inseparable, so the
	// whole automatic build is skipped — server_deploy does the full build+
	// restart on demand.
	//
	// BOTH kinds must set the pending gauge. PreinitPendingDeployGauge seeds
	// every manual repo at 0 regardless of kind, so a binary/static gate that
	// returned without setting it would leave the series reading 0 — not
	// "unknown" but an affirmative "nothing is waiting", while a release sat
	// undeployed. That is the quiet failure this gate's second half exists to
	// prevent, so it would defeat the feature exactly where nobody is looking.
	if req.Config.DeployOn == deployOnManual {
		slog.Info("deploy/manual-gate: automatic deploy withheld (deploy_on=manual); use server_deploy to deploy",
			"repo", req.Repo,
			"services", req.Config.Services,
			"kind", req.Config.resolvedKind(),
		)
		if req.Config.resolvedKind() == KindCompose {
			// Fall through to the compose build+publish path below, then gate
			// composeUp. Binary/static kinds have no separable artifact, so
			// they stop here.
		} else {
			// No artifact is built for these kinds, but the user-visible state
			// is identical to compose: a release happened and production did
			// not get it. server_deploy performs the full build+restart and
			// clears this.
			setPendingDeploy(req.Repo, req.Config.Services, 1)
			return BuildResult{
				Repo:        req.Repo,
				Services:    req.Config.Services,
				Success:     true,
				ManualGated: true,
			}
		}
	}

	// Non-compose kinds bypass the Docker Compose pipeline entirely.
	switch req.Config.resolvedKind() {
	case KindBinary:
		return executeBinaryBuild(ctx, req)
	case KindStatic:
		return executeStaticBuild(ctx, req)
	}

	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.Services,
	}

	// Step 0: wait for maintenance lock to clear
	if err := waitForMaintenanceLock(ctx, req.Config.Services); err != nil {
		result.Error = err.Error()
		return result
	}

	worktreePath, treeHash, worktreeCleanup, errMsg := gitPrepare(ctx, req.Config.SourcePath, req.CommitSHA)
	if errMsg != "" {
		result.Error = errMsg
		return result
	}
	defer worktreeCleanup()

	if errMsg := composeBuild(ctx, req, worktreePath, treeHash); errMsg != "" {
		result.Error = errMsg
		return result
	}

	// Image-cache push-after-build: tag and push the freshly-built image to
	// the registry under the tree-hash tag. Best-effort — push failure NEVER
	// fails the deploy (the image is already built and will be brought up),
	// but it MUST emit an ERROR-level log naming the tag and error so a
	// silently-failing push is observable.
	if treeHash != "" {
		pushCachedImages(ctx, req, treeHash)
	}

	// deploy_on: manual gate (compose) — the image is built/pulled and
	// published; withhold composeUp and record that a deployable artifact is
	// ready. The pending-deploy gauge makes a released-but-never-deployed fix
	// LOUD instead of silent (issue #183 half 2). composeUp + health + smoke
	// + rollback are all skipped — none of them can bypass this gate because
	// they live below it on the only automatic path.
	if req.Config.DeployOn == deployOnManual {
		setPendingDeploy(req.Repo, req.Config.Services, 1)
		slog.Info("deploy/manual-gate: artifact ready, deploy withheld (deploy_on=manual); use server_deploy to deploy",
			"repo", req.Repo,
			"services", req.Config.Services,
			"tree_hash", treeHash,
		)
		result.Success = true
		result.ManualGated = true
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
