package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
)

// staticScriptRunner is the function used to execute the static deploy script.
// It can be replaced in tests.
var staticScriptRunner = defaultStaticScriptRunner

// defaultStaticScriptRunner executes the deploy script directly (no shell
// interpolation) and returns the combined stdout+stderr.
// args[0] is the script path; DEPLOY_REPO_PATH and DEPLOY_SHA are set via Env.
func defaultStaticScriptRunner(ctx context.Context, script, repoPath, commitSHA string) ([]byte, error) {
	//nolint:gosec // script path comes from trusted deploy-repos.yaml, not user input
	cmd := exec.CommandContext(ctx, script)
	cmd.Env = append(cmd.Environ(),
		"DEPLOY_REPO_PATH="+repoPath,
		"DEPLOY_SHA="+commitSHA,
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

	out, err := staticScriptRunner(ctx, script, repoPath, req.CommitSHA)
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
