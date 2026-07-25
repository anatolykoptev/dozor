package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// -- DI seams (package-level vars, swapped in tests) --

// gitTreeHashRunner executes `git rev-parse HEAD^{tree}` in dir and returns
// the 40-char tree hash. Replaceable in tests.
var gitTreeHashRunner = defaultGitTreeHashRunner

//nolint:unused // DI default seam — assigned to var gitTreeHashRunner, swapped in tests
func defaultGitTreeHashRunner(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD^{tree}") //nolint:gosec // trusted local config
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD^{tree}: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// tokenCommandRunner runs a shell command whose STDOUT is a fresh registry
// token. The command is resolved per-deploy from ImageCacheConfig.TokenCommand
// (or the DOZOR_IMAGE_CACHE_TOKEN_CMD env fallback) — dozor does NOT hardcode
// any one token-minting script. The token is piped to docker login via stdin
// and is NEVER logged: on error only the command's exit status is reported,
// never its stdout (which IS the token). Replaceable in tests.
var tokenCommandRunner = defaultTokenCommandRunner

//nolint:unused // DI default seam — assigned to var tokenCommandRunner, swapped in tests
func defaultTokenCommandRunner(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // command is trusted operator config from deploy-repos.yaml / env
	out, err := cmd.Output()
	if err != nil {
		// Deliberately do NOT include `out` in the error: the command's
		// stdout is the token itself, and a token leaked into an error
		// string is a credential incident. Report only the exit error.
		return "", fmt.Errorf("token command %q: %w", command, err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("token command %q produced an empty token", command)
	}
	return token, nil
}

// dockerLoginRunner runs `docker login <registry> -u <username> --password-stdin`
// with the token piped via stdin (never on the command line, never logged).
// Replaceable in tests.
var dockerLoginRunner = defaultDockerLoginRunner

//nolint:unused // DI default seam — assigned to var dockerLoginRunner, swapped in tests
func defaultDockerLoginRunner(ctx context.Context, registry, username, token string) error {
	cmd := exec.CommandContext(ctx, "docker", "login", registry, "-u", username, "--password-stdin") //nolint:gosec // trusted local config
	cmd.Stdin = strings.NewReader(token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker login %s: %w: %s", registry, err, truncate(string(out), maxOutputLen))
	}
	return nil
}

// defaultTokenUsername is the default `docker login` username when neither
// per-repo token_username nor the DOZOR_IMAGE_CACHE_TOKEN_USERNAME env var is
// set. "x-access-token" is the GHCR convention for GitHub App installation
// tokens (the credential model used on this host); override for other registries.
const defaultTokenUsername = "x-access-token"

// registryHost extracts the registry host (the `docker login` target) from a
// fully-qualified image reference like "ghcr.io/anatolykoptev/oxpulse-chat".
// The host is everything before the first '/'. For a reference with no slash
// (e.g. "ubuntu") the whole string is returned (Docker treats it as a
// default-registry image, and `docker login ubuntu` is harmless/no-op for the
// ambient path which is the only path that reaches this with no slash).
func registryHost(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i]
	}
	return ref
}

// resolveTokenCommand returns the configured token-minting command for a repo:
// per-repo ImageCacheConfig.TokenCommand wins, then the
// DOZOR_IMAGE_CACHE_TOKEN_CMD env fallback, then "" (ambient-credential path).
func resolveTokenCommand(rc RepoConfig) string {
	if rc.ImageCache.TokenCommand != "" {
		return rc.ImageCache.TokenCommand
	}
	return os.Getenv("DOZOR_IMAGE_CACHE_TOKEN_CMD")
}

// resolveTokenUsername returns the username for `docker login`: per-repo
// ImageCacheConfig.TokenUsername wins, then the DOZOR_IMAGE_CACHE_TOKEN_USERNAME
// env fallback, then the default "x-access-token" (the GHCR App-token
// convention used on this host — overridable for other registries).
func resolveTokenUsername(rc RepoConfig) string {
	if rc.ImageCache.TokenUsername != "" {
		return rc.ImageCache.TokenUsername
	}
	if v := os.Getenv("DOZOR_IMAGE_CACHE_TOKEN_USERNAME"); v != "" {
		return v
	}
	return defaultTokenUsername
}

// authErrorIndicator reports whether a docker push/pull error string looks
// like a registry authentication failure (as opposed to a network or
// image-resolution failure). Used to classify the outcome metric so an auth
// failure is distinguishable from a generic "failed".
func authErrorIndicator(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "denied") ||
		strings.Contains(s, "authentication") ||
		strings.Contains(s, "no basic auth credentials") ||
		strings.Contains(s, "not authorized")
}

// authenticateRegistry obtains a fresh registry token (if a token command is
// configured) and runs `docker login <host> -u <user> --password-stdin`
// immediately before a push or a pull. Returns true when the caller may
// proceed with the push/pull; false when authentication failed and the caller
// must skip the push/pull (best-effort — a registry-auth failure never fails
// a deploy).
//
// When NO token command is configured (per-repo nor env), the function does
// NOT attempt a login and returns true, logging at INFO that the push/pull is
// relying on the ambient ~/.docker/config.json credential — so "we are relying
// on ambient credentials" is a stated fact rather than an accident. This
// preserves the previous behaviour for hosts that log in by other means.
//
// phase is "push" or "pull" — used in log lines and the auth-failure metric.
// The token is NEVER logged: not at any level, not in an error string, not in
// a command echo. The token command's stdout (which IS the token) is also
// never included in any error.
func authenticateRegistry(ctx context.Context, req BuildRequest, phase string) bool {
	tokenCmd := resolveTokenCommand(req.Config)
	if tokenCmd == "" {
		slog.Info("deploy: image cache "+phase+" relying on ambient docker config credential (no token_command configured)",
			"repo", req.Repo, "phase", phase)
		return true
	}

	token, err := tokenCommandRunner(ctx, tokenCmd)
	if err != nil {
		slog.Error("deploy: image cache "+phase+" authentication failed — token command error (best-effort, deploy continues)",
			"repo", req.Repo, "phase", phase, "error", err)
		ImageCacheAuthTotal.WithLabelValues(req.Repo, phase, "token_error").Inc()
		return false
	}
	// Defence in depth: trim and reject an empty token here (the choke point)
	// so a custom runner that returns whitespace-only is caught regardless of
	// whether the runner itself trims. The token command's stdout IS the
	// credential — never log it.
	token = strings.TrimSpace(token)
	if token == "" {
		slog.Error("deploy: image cache "+phase+" authentication failed — token command produced an empty token (best-effort, deploy continues)",
			"repo", req.Repo, "phase", phase, "token_command", tokenCmd)
		ImageCacheAuthTotal.WithLabelValues(req.Repo, phase, "token_error").Inc()
		return false
	}

	host := registryHost(req.Config.ImageCache.Registry)
	username := resolveTokenUsername(req.Config)
	if err := dockerLoginRunner(ctx, host, username, token); err != nil {
		slog.Error("deploy: image cache "+phase+" authentication failed — docker login rejected token (best-effort, deploy continues)",
			"repo", req.Repo, "phase", phase, "error", err)
		ImageCacheAuthTotal.WithLabelValues(req.Repo, phase, "login_error").Inc()
		return false
	}
	return true
}

// -- Tag format --

// cachedImageRef constructs the registry tag for a given tree hash.
// Format: <registry>:tree-<40-char-hex>
// Example: ghcr.io/anatolykoptev/oxpulse-chat:tree-72def7ea3afd8dd4c5aa384823cd97d534d01763
func cachedImageRef(registry, treeHash string) string {
	return registry + ":tree-" + treeHash
}

// -- Pull-before-build --

// tryPullCachedImage attempts to pull the tree-hash-tagged image from the
// registry and retag it to each service's compose-expected image name. Returns
// true (skip the build) only when ALL cacheable services are successfully
// pulled and retagged. Returns false on any failure — the caller falls through
// to the existing build path. This is the fallback guarantee: the change is
// never worse than the status quo.
//
// Only called when every service in the repo entry is cacheable (see
// allServicesCacheable); otherwise the non-cacheable services still need a
// build and there is no saving from skipping.
func tryPullCachedImage(ctx context.Context, req BuildRequest, treeHash string) bool {
	cacheable := req.Config.cacheableServices()
	if len(cacheable) == 0 || len(cacheable) != len(req.Config.Services) {
		return false
	}

	ref := cachedImageRef(req.Config.ImageCache.Registry, treeHash)

	// Authenticate against the registry immediately before the pull. A
	// registry-auth failure must never fail a deploy — on failure we fall
	// through to the build path (return false) with an auth_error metric so
	// the silent-expiry class is distinguishable from a network or
	// image-resolution failure.
	if !authenticateRegistry(ctx, req, "pull") {
		ImageCachePullTotal.WithLabelValues(req.Repo, "auth_error").Inc()
		return false
	}

	if err := runCmd(ctx, req.Config.ComposePath, "docker", "pull", ref); err != nil {
		if authErrorIndicator(err) {
			slog.Error("deploy: image reuse pull auth failure — falling back to build",
				"tree_hash", treeHash, "tag", ref, "error", err)
			ImageCachePullTotal.WithLabelValues(req.Repo, "auth_error").Inc()
			ImageCacheAuthTotal.WithLabelValues(req.Repo, "pull", "pull_auth").Inc()
		} else {
			slog.Info("deploy: image reuse miss — tree-"+treeHash+" not in registry, building from source",
				"tree_hash", treeHash, "tag", ref, "error", err)
			ImageCachePullTotal.WithLabelValues(req.Repo, "miss").Inc()
		}
		return false
	}

	for _, svc := range cacheable {
		imgName := composeImageName(ctx, req.Config.ComposePath, svc)
		if imgName == "" {
			slog.Error("deploy: image reuse — cannot resolve compose image name for retag, building from source",
				"service", svc, "tag", ref)
			ImageCachePullTotal.WithLabelValues(req.Repo, "error").Inc()
			return false
		}
		if err := runCmd(ctx, req.Config.ComposePath, "docker", "tag", ref, imgName); err != nil {
			slog.Error("deploy: image reuse — retag failed, building from source",
				"service", svc, "tag", ref, "target", imgName, "error", err)
			ImageCachePullTotal.WithLabelValues(req.Repo, "error").Inc()
			return false
		}
	}

	slog.Info("deploy: image reuse — pulled tree-"+treeHash+" from registry, skipping build",
		"services", cacheable, "tree_hash", treeHash, "tag", ref)
	ImageCachePullTotal.WithLabelValues(req.Repo, "reused").Inc()
	return true
}

// allServicesCacheable returns true when every service in the repo entry is
// cacheable (i.e. the cacheable set covers all services being built). This is
// the precondition for the pull-before-build path: if any service is not
// cacheable, it still needs a build, so skipping the entire build is wrong.
func allServicesCacheable(req BuildRequest) bool {
	cacheable := req.Config.cacheableServices()
	if len(cacheable) == 0 {
		return false
	}
	return len(cacheable) == len(req.Config.Services)
}

// -- Push-after-build --

// pushCachedImages tags and pushes each cacheable service's freshly-built
// image to the registry under the tree-hash tag. Best-effort: push failure
// NEVER fails the deploy (the image is already built locally and running),
// but it MUST emit an ERROR-level log naming the tag and the underlying error
// so a silently-failing push is observable. A silently-failing push means the
// whole optimisation quietly does nothing while looking healthy — the exact
// "green but doing nothing" class this fleet eliminated.
//
// A fresh registry token is obtained immediately before the push (the token
// may expire hourly; never rely on a persistent docker login). The token is
// piped via stdin to docker login — never written to a file, never logged.
// When no token command is configured, the push relies on the ambient
// ~/.docker/config.json credential (logged at INFO so it is a stated fact).
func pushCachedImages(ctx context.Context, req BuildRequest, treeHash string) {
	cacheable := req.Config.cacheableServices()
	if len(cacheable) == 0 {
		return
	}

	ref := cachedImageRef(req.Config.ImageCache.Registry, treeHash)

	// Obtain a fresh registry token and authenticate immediately before
	// pushing. On auth failure, skip the push entirely (best-effort) — the
	// image is already built and will be brought up; a registry-auth failure
	// must never fail a deploy.
	if !authenticateRegistry(ctx, req, "push") {
		ImageCachePushTotal.WithLabelValues(req.Repo, "auth_error").Inc()
		return
	}

	for _, svc := range cacheable {
		imgName := composeImageName(ctx, req.Config.ComposePath, svc)
		if imgName == "" {
			slog.Error("deploy: image cache push failed — cannot resolve compose image name (best-effort, deploy continues)",
				"tag", ref, "service", svc)
			ImageCachePushTotal.WithLabelValues(req.Repo, "image_name_error").Inc()
			continue
		}
		if err := runCmd(ctx, req.Config.ComposePath, "docker", "tag", imgName, ref); err != nil {
			slog.Error("deploy: image cache push failed — docker tag failed (best-effort, deploy continues)",
				"tag", ref, "service", svc, "source", imgName, "error", err)
			ImageCachePushTotal.WithLabelValues(req.Repo, "tag_error").Inc()
			continue
		}
		if err := runCmd(ctx, req.Config.ComposePath, "docker", "push", ref); err != nil {
			if authErrorIndicator(err) {
				slog.Error("deploy: image cache push failed — registry auth rejected push (best-effort, deploy continues)",
					"tag", ref, "service", svc, "error", err)
				ImageCachePushTotal.WithLabelValues(req.Repo, "push_auth_error").Inc()
				ImageCacheAuthTotal.WithLabelValues(req.Repo, "push", "push_auth").Inc()
			} else {
				slog.Error("deploy: image cache push failed — docker push rejected (best-effort, deploy continues)",
					"tag", ref, "service", svc, "error", err)
				ImageCachePushTotal.WithLabelValues(req.Repo, "push_error").Inc()
			}
			continue
		}
		slog.Info("deploy: image cache pushed",
			"tag", ref, "service", svc, "tree_hash", treeHash)
		ImageCachePushTotal.WithLabelValues(req.Repo, "pushed").Inc()
	}
}
