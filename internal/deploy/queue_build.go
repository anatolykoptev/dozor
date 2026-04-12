package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func (q *Queue) executeBuild(ctx context.Context, req BuildRequest) BuildResult {
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.Services,
	}

	if errMsg := gitPull(ctx, req.Config.SourcePath); errMsg != "" {
		result.Error = errMsg
		return result
	}

	if errMsg := composeBuild(ctx, req); errMsg != "" {
		result.Error = errMsg
		return result
	}

	if errMsg := composeUp(ctx, req); errMsg != "" {
		result.Error = errMsg
		return result
	}

	time.Sleep(healthWait)
	for _, svc := range req.Config.Services {
		if err := checkHealth(ctx, req.Config.ComposePath, svc); err != nil {
			result.Error = fmt.Sprintf("health check %s: %v", svc, err)
			return result
		}
	}

	if err := smokeTest(ctx, req.Config.SmokeURL); err != nil {
		result.Error = fmt.Sprintf("smoke test: %v", err)
		return result
	}

	result.Success = true
	return result
}

func gitPull(ctx context.Context, sourcePath string) string {
	if sourcePath == "" {
		return ""
	}
	if err := runCmd(ctx, sourcePath, "git", "fetch", "origin", "main"); err != nil {
		return fmt.Sprintf("git fetch: %v", err)
	}
	if err := runCmd(ctx, sourcePath, "git", "reset", "--hard", "origin/main"); err != nil {
		return fmt.Sprintf("git reset: %v", err)
	}
	return ""
}

func composeBuild(ctx context.Context, req BuildRequest) string {
	imagesBefore := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)

	buildArgs := []string{"compose", "build"}
	if req.Config.NoCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, req.Config.Services...)

	if err := runCmd(ctx, req.Config.ComposePath, "docker", buildArgs...); err != nil {
		return fmt.Sprintf("docker build: %v", err)
	}

	imagesAfter := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)
	logImageDiff(imagesBefore, imagesAfter, req.Config.Services, req.CommitSHA)
	return ""
}

func composeUp(ctx context.Context, req BuildRequest) string {
	upArgs := append(
		[]string{"compose", "up", "-d", "--no-deps", "--force-recreate"},
		req.Config.Services...)

	var upErr error
	for attempt := 1; attempt <= upMaxRetries; attempt++ {
		upErr = runCmd(ctx, req.Config.ComposePath, "docker", upArgs...)
		if upErr == nil {
			return ""
		}
		slog.Warn("deploy: docker up failed, retrying",
			"attempt", attempt,
			"max", upMaxRetries,
			"error", truncate(upErr.Error(), maxOutputLen),
			"services", req.Config.Services,
		)
		if attempt < upMaxRetries {
			if ctx.Err() != nil {
				return fmt.Sprintf("docker up: context cancelled during retry: %v", upErr)
			}
			select {
			case <-ctx.Done():
				return fmt.Sprintf("docker up: context cancelled during retry: %v", upErr)
			case <-time.After(upRetryDelay):
			}
		}
	}
	return fmt.Sprintf("docker up (after %d attempts): %v", upMaxRetries, upErr)
}
