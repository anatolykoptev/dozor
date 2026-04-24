package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

const binaryHealthWait = 3 * time.Second

// executeBinaryBuild handles the "binary" deploy kind:
//  1. git pull --ff-only in SourcePath
//  2. run BuildCmd in SourcePath (e.g. go build -o /usr/local/bin/svc ./cmd/svc)
//  3. systemctl --user restart each UserService
//  4. optional smoke URL check
func executeBinaryBuild(ctx context.Context, req BuildRequest) BuildResult {
	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.UserServices,
	}

	// Step 1: pull latest source.
	slog.Info("deploy/binary: git pull", "repo", req.Repo, "path", req.Config.SourcePath)
	if err := runCmd(ctx, req.Config.SourcePath, "git", "pull", "--ff-only"); err != nil {
		result.Error = fmt.Sprintf("git pull: %v", err)
		return result
	}

	// Step 2: build binary.
	cmd := req.Config.BuildCmd
	slog.Info("deploy/binary: build", "repo", req.Repo, "cmd", cmd)
	if err := runCmd(ctx, req.Config.SourcePath, cmd[0], cmd[1:]...); err != nil {
		result.Error = fmt.Sprintf("build: %v", err)
		return result
	}

	// Step 3: restart systemd user services.
	for _, svc := range req.Config.UserServices {
		slog.Info("deploy/binary: restart service", "service", svc)
		//nolint:gosec // svc comes from trusted deploy-repos.yaml, not user input
		out, err := exec.CommandContext(ctx, "systemctl", "--user", "restart", svc).CombinedOutput()
		if err != nil {
			result.Error = fmt.Sprintf("restart %s: %v: %s", svc, err, out)
			return result
		}
	}

	// Step 4: brief settle + smoke test.
	time.Sleep(binaryHealthWait)
	if err := smokeTest(ctx, req.Config.SmokeURL); err != nil {
		result.Error = fmt.Sprintf("smoke test: %v", err)
		return result
	}

	result.Success = true
	return result
}
