package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// gitPrepare fetches the target commit and creates a temporary git worktree for building.
// Returns the worktree path (which must be cleaned up after build) and a cleanup function.
// The developer's working directory is never modified.
func gitPrepare(ctx context.Context, sourcePath, commitSHA string) (worktreePath string, cleanup func(), errMsg string) {
	noop := func() {}
	if sourcePath == "" {
		return "", noop, ""
	}
	if err := runCmd(ctx, sourcePath, "git", "fetch", "origin"); err != nil {
		return "", noop, fmt.Sprintf("git fetch: %v", err)
	}

	// Determine target ref: exact SHA if provided, otherwise latest from default branch.
	var target string
	if len(commitSHA) >= 7 { //nolint:mnd
		target = commitSHA
	} else {
		branch := detectDefaultBranch(ctx, sourcePath)
		target = "origin/" + branch
	}

	// Create a temporary worktree for this build.
	shortSHA := commitSHA
	if len(shortSHA) > 8 { //nolint:mnd
		shortSHA = shortSHA[:8]
	}
	if shortSHA == "" {
		shortSHA = "latest"
	}
	wtPath := fmt.Sprintf("/tmp/deploy-%s-%d", shortSHA, time.Now().UnixMilli())

	if err := runCmd(ctx, sourcePath, "git", "worktree", "add", "--detach", wtPath, target); err != nil {
		return "", noop, fmt.Sprintf("git worktree add: %v", err)
	}

	cleanupFn := func() {
		if err := runCmd(context.Background(), sourcePath, "git", "worktree", "remove", "--force", wtPath); err != nil {
			slog.Warn("deploy: worktree cleanup failed, removing manually", "path", wtPath, "error", err)
			os.RemoveAll(wtPath)
		}
	}

	slog.Info("deploy: worktree created", "path", wtPath, "target", target)
	return wtPath, cleanupFn, ""
}

// detectDefaultBranch returns "main" or "master" based on which remote branch exists.
func detectDefaultBranch(ctx context.Context, sourcePath string) string {
	if err := runCmd(ctx, sourcePath, "git", "rev-parse", "--verify", "origin/main"); err == nil {
		return "main"
	}
	return "master"
}

// composeBuild runs docker compose build with optional --no-cache.
// Snapshots images before/after to detect no-op builds.
// If worktreePath is non-empty, a temporary compose override redirects the build
// context for all target services to the worktree directory.
func composeBuild(ctx context.Context, req BuildRequest, worktreePath string) string {
	imagesBefore := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)

	buildArgs := []string{"compose"}

	// Generate a temporary override file that remaps build.context to the worktree.
	if worktreePath != "" {
		overridePath, err := writeBuildContextOverride(req.Config.Services, worktreePath)
		if err != nil {
			return fmt.Sprintf("compose override: %v", err)
		}
		defer os.Remove(overridePath)
		buildArgs = append(buildArgs, "-f", "docker-compose.yml", "-f", overridePath)
	}

	buildArgs = append(buildArgs, "build")
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

// writeBuildContextOverride creates a temporary docker-compose override YAML that
// redirects build.context for each service to the given worktree path.
// Returns the path to the temp file (caller must remove it).
func writeBuildContextOverride(services []string, worktreePath string) (string, error) {
	f, err := os.CreateTemp("", "dozor-compose-override-*.yml")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	// Write a minimal compose override that only overrides build.context.
	fmt.Fprintln(f, "services:")
	for _, svc := range services {
		fmt.Fprintf(f, "  %s:\n    build:\n      context: %s\n", svc, worktreePath)
	}

	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write override file: %w", err)
	}
	return f.Name(), nil
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
