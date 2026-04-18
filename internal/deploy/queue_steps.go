package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
// context for all target services to the worktree directory — preserving each
// service's original subdirectory offset relative to sourcePath.
func composeBuild(ctx context.Context, req BuildRequest, worktreePath string) string {
	imagesBefore := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)

	buildArgs := []string{"compose"}

	// Generate a temporary override file that remaps build.context to the worktree.
	if worktreePath != "" {
		overrides, err := resolveBuildOverrides(
			ctx,
			req.Config.ComposePath,
			req.Config.SourcePath,
			req.Config.Services,
			worktreePath,
		)
		if err != nil {
			return fmt.Sprintf("resolve overrides: %v", err)
		}
		overridePath, err := writeBuildContextOverride(overrides)
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

// BuildOverride describes the build.context rewrite for a single service in
// the temporary docker-compose override file.
type BuildOverride struct {
	Service string
	Context string // absolute path: worktreePath joined with the original subdir offset
}

// resolveBuildOverrides reads the compose at composePath, finds each service's
// original build.context via `docker compose config --format json`, computes
// its relative offset from sourcePath, and returns one BuildOverride per
// service with the context rebased onto worktreePath.
//
// If a service's original context equals sourcePath exactly (common case —
// repo-root-as-context), the override is simply worktreePath.
// If a service's context lives outside sourcePath, an error is returned so
// the deploy fails loudly instead of silently using a wrong path.
func resolveBuildOverrides(
	ctx context.Context,
	composePath, sourcePath string,
	services []string,
	worktreePath string,
) ([]BuildOverride, error) {
	out, err := outputRunner(ctx, composePath, "docker", "compose", "config", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker compose config: %w", err)
	}

	contexts, err := parseComposeContexts(out, services)
	if err != nil {
		return nil, err
	}

	overrides := make([]BuildOverride, 0, len(services))
	for _, svc := range services {
		origCtx := contexts[svc]
		rel, err := filepath.Rel(sourcePath, origCtx)
		if err != nil {
			return nil, fmt.Errorf("service %q: cannot relativize %q against source %q: %w",
				svc, origCtx, sourcePath, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("service %q: build.context %q is outside source_path %q",
				svc, origCtx, sourcePath)
		}
		newCtx := worktreePath
		if rel != "." {
			newCtx = filepath.Join(worktreePath, rel)
		}
		overrides = append(overrides, BuildOverride{Service: svc, Context: newCtx})
	}
	return overrides, nil
}

// parseComposeContexts decodes the JSON output of `docker compose config
// --format json` and returns a map of service → build.context for each of
// the requested services. Returns an error if any requested service is
// missing or has no build.context.
func parseComposeContexts(configJSON []byte, services []string) (map[string]string, error) {
	var parsed struct {
		Services map[string]struct {
			Build *struct {
				Context string `json:"context"`
			} `json:"build"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &parsed); err != nil {
		return nil, fmt.Errorf("parse compose config: %w", err)
	}

	result := make(map[string]string, len(services))
	for _, svc := range services {
		entry, ok := parsed.Services[svc]
		if !ok {
			return nil, fmt.Errorf("service %q not found in compose config", svc)
		}
		if entry.Build == nil || entry.Build.Context == "" {
			return nil, fmt.Errorf("service %q has no build.context in compose config", svc)
		}
		result[svc] = entry.Build.Context
	}
	return result, nil
}

// writeBuildContextOverride creates a temporary docker-compose override YAML that
// redirects build.context for each service to the given absolute path.
// Returns the path to the temp file (caller must remove it).
func writeBuildContextOverride(overrides []BuildOverride) (string, error) {
	f, err := os.CreateTemp("", "dozor-compose-override-*.yml")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	// Write a minimal compose override that only overrides build.context.
	fmt.Fprintln(f, "services:")
	for _, o := range overrides {
		fmt.Fprintf(f, "  %s:\n    build:\n      context: %s\n", o.Service, o.Context)
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
