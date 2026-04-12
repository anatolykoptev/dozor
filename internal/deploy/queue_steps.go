package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// gitPull fetches and resets to origin/main. Returns error message or "".
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

// composeBuild runs docker compose build with optional --no-cache.
// Snapshots images before/after to detect no-op builds.
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

// composeUp runs docker compose up with retry on transient failure.
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

// pruneOldImages removes dangling images and build cache older than 24h.
// Errors are logged but never fail the deploy.
func pruneOldImages(ctx context.Context, composePath string) {
	if err := runCmd(ctx, composePath, "docker", "image", "prune", "-f"); err != nil {
		slog.Warn("deploy: image prune failed", "error", err)
	}
	if err := runCmd(ctx, composePath, "docker", "builder", "prune", "-f", "--filter", "until=24h"); err != nil {
		slog.Warn("deploy: builder prune failed", "error", err)
	}
}
