package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// gitPrepare fetches the target commit and creates a temporary git worktree for building.
// Returns the worktree path, the git tree hash of the target commit (used for
// image-cache tagging), a cleanup function, and an error message (empty on success).
// The developer's working directory is never modified.
//
// The tree hash (git rev-parse <target>^{tree}) is the content-address of the
// source tree — two commits with identical trees (e.g. a dev→main merge) share
// the same tree hash. It is the registry tag key for build-once-promote. When
// the tree hash cannot be resolved (rare git error), it is returned empty and
// image caching is silently disabled for this deploy (the build proceeds as
// today, push/pull are skipped).
func gitPrepare(ctx context.Context, sourcePath, commitSHA string) (worktreePath, treeHash string, cleanup func(), errMsg string) {
	noop := func() {}
	if sourcePath == "" {
		return "", "", noop, ""
	}
	if err := withFetchLock(ctx, sourcePath, func() error {
		return runCmd(ctx, sourcePath, "git", "fetch", "origin")
	}); err != nil {
		return "", "", noop, fmt.Sprintf("git fetch: %v", err)
	}

	// Determine target ref: exact SHA if provided, otherwise latest from default branch.
	var target string
	if len(commitSHA) >= 7 { //nolint:mnd
		target = commitSHA
		// #151: a release event may reference a SHA that the generic refspec
		// fetch above has not yet brought into the local clone (debounce race
		// when two releases land close together). Fetch the exact SHA so the
		// object is present locally; GitHub's uploadpack allows fetch-by-SHA.
		// If the server rejects fetch-by-SHA, fall through to the resolvability
		// guard below — it fails fast with a clear error instead of the opaque
		// "fatal: invalid reference" from git worktree add.
		if err := withFetchLock(ctx, sourcePath, func() error {
			return runCmd(ctx, sourcePath, "git", "fetch", "origin", commitSHA)
		}); err != nil {
			slog.Warn("deploy: targeted SHA fetch failed; relying on generic fetch + resolvability guard",
				"sha", commitSHA, "error", err)
		}
		// Guard: verify the SHA resolves to a commit before attempting the
		// worktree add. Fail fast with an actionable message instead of the
		// raw "invalid reference" git error.
		if err := runCmd(ctx, sourcePath, "git", "cat-file", "-e", commitSHA+"^{commit}"); err != nil {
			return "", "", noop, fmt.Sprintf(
				"commit %s is not present in the local clone after git fetch origin; "+
					"the release SHA was not fetched yet (debounce race between close releases?). "+
					"Verify the SHA exists on the remote and re-trigger the deploy.",
				commitSHA,
			)
		}
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
		return "", "", noop, fmt.Sprintf("git worktree add: %v", err)
	}

	// Resolve the tree hash of the target commit for image-cache tagging.
	// The worktree is detached at `target`, so HEAD^{tree} is the tree hash.
	// On failure, return empty — image caching is silently disabled for this
	// deploy (build proceeds as today, push/pull are skipped).
	resolvedTreeHash, treeErr := gitTreeHashRunner(ctx, wtPath)
	if treeErr != nil {
		slog.Warn("deploy: cannot resolve tree hash for image cache; feature disabled for this deploy",
			"path", wtPath, "target", target, "error", treeErr)
		resolvedTreeHash = ""
	}

	cleanupFn := func() {
		if err := runCmd(context.Background(), sourcePath, "git", "worktree", "remove", "--force", wtPath); err != nil {
			slog.Warn("deploy: worktree cleanup failed, removing manually", "path", wtPath, "error", err)
			os.RemoveAll(wtPath)
		}
	}

	slog.Info("deploy: worktree created", "path", wtPath, "target", target, "tree_hash", resolvedTreeHash)
	return wtPath, resolvedTreeHash, cleanupFn, ""
}

// detectDefaultBranch returns "main" or "master" based on which remote branch exists.
func detectDefaultBranch(ctx context.Context, sourcePath string) string { //nolint:goconst // "main" vs "master" are branch names, not the deploy default
	if err := runCmd(ctx, sourcePath, "git", "rev-parse", "--verify", "origin/main"); err == nil {
		return "main" //nolint:goconst
	}
	return "master"
}

// composeBuild runs docker compose build with optional --no-cache.
// Snapshots images before/after to detect no-op builds.
// If worktreePath is non-empty, a temporary compose override redirects the build
// context for all target services to the worktree directory — preserving each
// service's original subdirectory offset relative to sourcePath.
//
// Before building, two additional steps run:
//  1. If DeployClonePath is set, the deploy clone is auto-pulled to
//     origin/<branch> so the compose config is never stale.
//  2. OXPULSE_GIT_SHA and BUILD_TIMESTAMP build-args are injected so
//     Dockerfiles that declare these ARGs get the correct values baked in.
func composeBuild(ctx context.Context, req BuildRequest, worktreePath, treeHash string) string {
	// Part A: auto-pull the deploy clone before reading its compose config.
	branch := req.Config.Branch
	if branch == "" {
		branch = "main"
	}
	pullDeployClone(ctx, req.Repo, req.Config.DeployClonePath, branch)

	// Run pre-build script if configured. Used for building web OCI artifacts
	// that the main Dockerfile consumes via WEB_ARTIFACT_IMAGE build-arg.
	// The script runs in SourcePath with DEPLOY_REPO_PATH and DEPLOY_SHA env
	// vars. A non-zero exit aborts the build.
	if req.Config.PreBuildScript != "" {
		shaDir := worktreePath
		if shaDir == "" {
			shaDir = req.Config.SourcePath
		}
		slog.Info("deploy: running pre-build script",
			"script", req.Config.PreBuildScript,
			"repo", req.Repo,
			"commit", short(req.CommitSHA),
		)
		if errMsg := runPreBuildScript(ctx, req.Config.PreBuildScript, shaDir, req.CommitSHA); errMsg != "" {
			return errMsg
		}
	}

	// Invalidate BuildKit exec cache mounts when requested (Rust services with
	// --mount=type=cache,target=target/ — see RepoConfig.PruneBuildkitCache).
	pruneBuildkitCacheMount(ctx, req)

	// Image-cache pull-before-build: if the repo opts in (image_cache.registry
	// set) and every service being built is cacheable, try to pull the
	// tree-hash-tagged image from the registry. On a hit, retag to each
	// service's compose-expected image name and skip the build entirely.
	// On ANY failure (not found, registry down, auth error, timeout, retag
	// error), fall through to the existing build path — never worse than the
	// status quo.
	if treeHash != "" && allServicesCacheable(req) {
		if tryPullCachedImage(ctx, req, treeHash) {
			return ""
		}
	}

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

		slog.Info("deploy: build context override",
			"services", req.Config.Services,
			"worktree", worktreePath,
			"override_path", overridePath,
			"overrides", overrides,
		)
	}

	buildArgs = append(buildArgs, "build")
	if req.Config.NoCache {
		buildArgs = append(buildArgs, "--no-cache")
	}

	// Part B: inject build-time env vars so Dockerfiles that declare
	// ARG OXPULSE_GIT_SHA / ARG BUILD_TIMESTAMP get the right values.
	// worktreePath is the source worktree; fall back to SourcePath if absent.
	shaDir := worktreePath
	if shaDir == "" {
		shaDir = req.Config.SourcePath
	}
	gitSHA := resolveGitSHA(ctx, shaDir)
	buildTimestamp := strconv.FormatInt(time.Now().Unix(), 10) //nolint:mnd // base-10 decimal
	buildArgs = append(buildArgs,
		"--build-arg", "OXPULSE_GIT_SHA="+gitSHA,
		"--build-arg", "BUILD_TIMESTAMP="+buildTimestamp,
	)

	// Inject per-repo extra build args with ${SHA} placeholder substitution.
	// ${SHA} resolves to the 12-char artifact tag derived from the FULL 40-char
	// commit SHA via artifactTagSHA — the single helper used by both lanes.
	// This ensures cross-lane tag parity: a manual-lane deploy and a webhook-lane
	// deploy of the SAME commit derive the SAME artifact tag. A short or invalid
	// SHA is REJECTED here (deploy fails) rather than silently truncated — a
	// silent [:12] on a 9-char string was the root cause of the cross-lane
	// tag mismatch bug.
	// Used for passing pre-built artifact image tags
	// (e.g. WEB_ARTIFACT_IMAGE=oxpulse-chat-web:prod-${SHA}).
	var shortSHA12 string
	if len(req.Config.BuildArgs) > 0 {
		tag, err := artifactTagSHA(req.CommitSHA)
		if err != nil {
			return fmt.Sprintf("artifact tag derivation: %v", err)
		}
		shortSHA12 = tag
	}
	for _, arg := range req.Config.BuildArgs {
		expanded := strings.ReplaceAll(arg, "${SHA}", shortSHA12)
		buildArgs = append(buildArgs, "--build-arg", expanded)
	}

	buildArgs = append(buildArgs, req.Config.Services...)

	if errMsg := runBuildWithFullLog(ctx, req, buildArgs); errMsg != "" {
		return errMsg
	}

	imagesAfter := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)
	if errMsg := logImageDiff(imagesBefore, imagesAfter, req.Config.Services, req.CommitSHA); errMsg != "" {
		return errMsg
	}
	return ""
}

// buildRunner invokes `docker build` and returns the full combined output.
// It is a package-level var so tests can substitute it. The default
// implementation is in queue_helpers.go (defaultBuildRunner).
var buildRunner = defaultBuildRunner

// upRunner invokes `docker compose up` and returns the full combined output.
// It is a package-level var so tests can substitute it. The default
// implementation is in queue_helpers.go (defaultUpRunner).
var upRunner = defaultUpRunner

// runBuildWithFullLog invokes docker build via buildRunner and, on failure,
// dumps the full combined stderr to /tmp/dozor-build-<shortSHA>-<ts>.log.
// runCmd truncates output to maxOutputLen, which previously masked Docker's
// real complaint (e.g. the truncated "transferring dockerfile: 2B done"
// that hid the subdir-context bug).
func runBuildWithFullLog(ctx context.Context, req BuildRequest, buildArgs []string) string {
	output, err := buildRunner(ctx, req.Config.ComposePath, buildArgs)
	if err == nil {
		return ""
	}

	dumpPath := fmt.Sprintf("/tmp/dozor-build-%s-%d.log", short(req.CommitSHA), time.Now().UnixMilli())
	if werr := os.WriteFile(dumpPath, output, 0o600); werr != nil { //nolint:mnd // standard log-file mode
		slog.Warn("deploy: failed to dump full build log", "path", dumpPath, "error", werr)
		dumpPath = ""
	}

	slog.Error("deploy: docker build failed",
		"services", req.Config.Services,
		"commit", short(req.CommitSHA),
		"err", err,
		"stderr_tail", truncate(string(output), maxOutputLen),
		"full_log_path", dumpPath,
	)

	if dumpPath != "" {
		return fmt.Sprintf("docker build: %v (full log: %s)", err, dumpPath)
	}
	return fmt.Sprintf("docker build: %v: %s", err, truncate(string(output), maxOutputLen))
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

// runUpWithFullLog invokes docker compose up via upRunner and, on failure,
// dumps the full combined stderr to /tmp/dozor-up-<deployID>-<ts>.log.
// runCmd truncates output to maxUpOutputLen, which previously masked Docker's
// real error (e.g. "Container name already in use") behind env-var warnings.
func runUpWithFullLog(ctx context.Context, req BuildRequest, deployID string) string {
	upArgs := append(
		[]string{"compose", "up", "-d", "--no-deps", "--force-recreate"},
		req.Config.Services...)

	output, err := upRunner(ctx, req.Config.ComposePath, upArgs)
	if err == nil {
		return ""
	}

	dumpPath := fmt.Sprintf("/tmp/dozor-up-%s-%d.log", deployID, time.Now().UnixMilli())
	if werr := os.WriteFile(dumpPath, output, 0o600); werr != nil { //nolint:mnd // standard log-file mode
		slog.Warn("deploy: failed to dump full up log", "path", dumpPath, "error", werr)
		dumpPath = ""
	}

	slog.Error("deploy: docker up failed",
		"services", req.Config.Services,
		"deploy_id", deployID,
		"err", err,
		"stderr_tail", truncate(string(output), maxUpOutputLen),
		"full_log_path", dumpPath,
	)

	if dumpPath != "" {
		return fmt.Sprintf("docker up: %v (full log: %s)", err, dumpPath)
	}
	return fmt.Sprintf("docker up: %v: %s", err, truncate(string(output), maxUpOutputLen))
}

// composeUp runs docker compose up with retry on transient failure.
// Each failed attempt dumps the full stderr to a /tmp/dozor-up-*.log file
// so operators can see the real error (e.g. container conflict) past warnings.
func composeUp(ctx context.Context, req BuildRequest) string {
	deployID := short(req.CommitSHA)

	var lastErrMsg string
	for attempt := 1; attempt <= upMaxRetries; attempt++ {
		lastErrMsg = runUpWithFullLog(ctx, req, deployID)
		if lastErrMsg == "" {
			return ""
		}
		slog.Warn("deploy: docker up failed, retrying",
			"attempt", attempt,
			"max", upMaxRetries,
			"error", lastErrMsg,
			"services", req.Config.Services,
		)
		if attempt < upMaxRetries {
			if ctx.Err() != nil {
				return fmt.Sprintf("docker up: context cancelled during retry: %v", lastErrMsg)
			}
			select {
			case <-ctx.Done():
				return fmt.Sprintf("docker up: context cancelled during retry: %v", lastErrMsg)
			case <-time.After(upRetryDelay):
			}
		}
	}
	return fmt.Sprintf("docker up (after %d attempts): %v", upMaxRetries, lastErrMsg)
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

// runPreBuildScript executes a pre-build bash script with DEPLOY_REPO_PATH
// and DEPLOY_SHA env vars set. The script runs in sourceDir. A non-zero exit
// code returns an error message string (not a Go error) to match the
// composeBuild return convention.
//
// DEPLOY_SHA contract: the value MUST be a full 40-char hex commit SHA.
// Pre-build scripts in another repo do `SHORT_SHA="${SHA:0:12}"` — a short
// SHA here makes that a no-op and produces a tag that doesn't match the
// webhook lane's 12-char tag. validateDeploySHA enforces this at the shell
// boundary so a short value can never leave dozor.
func runPreBuildScript(ctx context.Context, scriptPath, sourceDir, commitSHA string) string {
	if errMsg := validateDeploySHA(commitSHA); errMsg != "" {
		return errMsg
	}
	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Dir = sourceDir
	cmd.Env = append(os.Environ(),
		"DEPLOY_REPO_PATH="+sourceDir,
		"DEPLOY_SHA="+commitSHA,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("deploy: pre-build script failed",
			"script", scriptPath,
			"error", err,
			"output", string(out),
		)
		return fmt.Sprintf("pre-build script %s failed: %v\n%s", scriptPath, err, out)
	}
	slog.Info("deploy: pre-build script completed",
		"script", scriptPath,
		"output_tail", lastLines(string(out), 5), //nolint:mnd // last 5 lines for context
	)
	return ""
}

// lastLines returns the last n lines of s (for log truncation).
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
