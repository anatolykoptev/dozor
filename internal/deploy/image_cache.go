package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

// ghAppTokenRunner mints a GitHub App installation token by running
// ~/bin/gh-app-token.sh. The token has ~1h TTL. Replaceable in tests.
var ghAppTokenRunner = defaultGHAppTokenRunner

//nolint:unused // DI default seam — assigned to var ghAppTokenRunner, swapped in tests
func defaultGHAppTokenRunner(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve $HOME for gh-app-token.sh: %w", err)
	}
	scriptPath := filepath.Join(home, "bin", "gh-app-token.sh")
	cmd := exec.CommandContext(ctx, scriptPath) //nolint:gosec // trusted local script
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh-app-token.sh: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gh-app-token.sh produced an empty token")
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
	if err := runCmd(ctx, req.Config.ComposePath, "docker", "pull", ref); err != nil {
		slog.Info("deploy: image reuse miss — tree-"+treeHash+" not in registry, building from source",
			"tree_hash", treeHash, "tag", ref, "error", err)
		ImageCachePullTotal.WithLabelValues(req.Repo, "miss").Inc()
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
// The GH App token is minted fresh immediately before the push (it expires
// hourly; never rely on a persistent docker login). The token is piped via
// stdin to docker login — never written to a file, never logged.
func pushCachedImages(ctx context.Context, req BuildRequest, treeHash string) {
	cacheable := req.Config.cacheableServices()
	if len(cacheable) == 0 {
		return
	}

	ref := cachedImageRef(req.Config.ImageCache.Registry, treeHash)

	// Mint a fresh GH App token and authenticate immediately before pushing.
	// The token expires hourly; do NOT rely on a persistent docker login.
	token, err := ghAppTokenRunner(ctx)
	if err != nil {
		slog.Error("deploy: image cache push failed — cannot mint GH App token (best-effort, deploy continues)",
			"tag", ref, "error", err)
		ImageCachePushTotal.WithLabelValues(req.Repo, "token_error").Inc()
		return
	}
	if err := dockerLoginRunner(ctx, "ghcr.io", "x-access-token", token); err != nil {
		slog.Error("deploy: image cache push failed — docker login rejected token (best-effort, deploy continues)",
			"tag", ref, "error", err)
		ImageCachePushTotal.WithLabelValues(req.Repo, "login_error").Inc()
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
			slog.Error("deploy: image cache push failed — docker push rejected (best-effort, deploy continues)",
				"tag", ref, "service", svc, "error", err)
			ImageCachePushTotal.WithLabelValues(req.Repo, "push_error").Inc()
			continue
		}
		slog.Info("deploy: image cache pushed",
			"tag", ref, "service", svc, "tree_hash", treeHash)
		ImageCachePushTotal.WithLabelValues(req.Repo, "pushed").Inc()
	}
}
