package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// staticScriptRunner is the function used to execute the static deploy script.
// It can be replaced in tests.
var staticScriptRunner = defaultStaticScriptRunner

// defaultStaticScriptRunner executes the deploy script directly (no shell
// interpolation) and returns the combined stdout+stderr.
//
// Scripts execute with cwd set to the deploy worktree root (repoPath).
// DEPLOY_REPO_PATH is also set to the same value for legacy compatibility —
// scripts that relied on `cd "${DEPLOY_REPO_PATH}"` continue to work, but
// the cwd default is now safe even for scripts that omit that line.
//
// Environment variables provided to the script:
//
//	DEPLOY_REPO_PATH      — absolute path to the local git checkout (SourcePath)
//	DEPLOY_SHA            — commit SHA from the webhook
//	DEPLOY_CHANGED_PATHS  — newline-separated list of changed file paths across
//	                        all commits in the push (union across coalesced events).
//	                        Empty string when unknown (force-push or oversized push):
//	                        scripts must treat empty as "all paths changed" and act
//	                        conservatively.
func defaultStaticScriptRunner(ctx context.Context, script, repoPath, commitSHA string, changedPaths []string) ([]byte, error) {
	//nolint:gosec // script path comes from trusted deploy-repos.yaml, not user input
	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = repoPath
	changedPathsVal := strings.Join(changedPaths, "\n")
	cmd.Env = append(cmd.Environ(),
		"DEPLOY_REPO_PATH="+repoPath,
		"DEPLOY_SHA="+commitSHA,
		"DEPLOY_CHANGED_PATHS="+changedPathsVal,
	)
	return cmd.CombinedOutput()
}

// executeStaticBuild handles the "static" deploy kind:
//  1. Runs StaticDeployScript with DEPLOY_REPO_PATH and DEPLOY_SHA in the environment.
//  2. Captures stdout+stderr and logs them.
//  3. Returns success/failure based on the script exit code.
func executeStaticBuild(ctx context.Context, req BuildRequest) BuildResult {
	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.Services,
	}

	script := req.Config.StaticDeployScript
	repoPath := req.Config.SourcePath

	slog.Info("deploy/static: running deploy script",
		"repo", req.Repo,
		"script", script,
		"repo_path", repoPath,
		"commit", short(req.CommitSHA),
	)

	out, err := staticScriptRunner(ctx, script, repoPath, req.CommitSHA, req.ChangedPaths)
	if len(out) > 0 {
		slog.Info("deploy/static: script output",
			"repo", req.Repo,
			"output", truncate(string(out), maxOutputLen),
		)
	}
	if err != nil {
		result.Error = fmt.Sprintf("static deploy script %s: %v: %s",
			script, err, truncate(string(out), maxOutputLen))
		return result
	}

	result.Success = true
	return result
}
