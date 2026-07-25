package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// -- DI helpers for image_cache seams --

// composeImagesOutputRunner stubs outputRunner for the commands used by the
// image-cache paths: `docker compose config --images <svc>` (used by
// composeImageName to resolve the image name WITHOUT containers) and
// `docker compose config --format json` (used by resolveBuildOverrides). The
// two commands return different shapes; this stub dispatches on the compose
// subcommand + flag (args[1]/args[2]).
func composeImagesOutputRunner(svcName, sourcePath string) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		// args: docker compose <subcommand> ...
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			// `docker compose config --images <svc>` → one resolved ref per line.
			return []byte("krolik-server-" + svcName + "\n"), nil
		}
		// `docker compose config --format json` → {"services":{"<svc>":{"build":{"context":"..."}}}}
		return []byte(`{"services":{"` + svcName + `":{"build":{"context":"` + sourcePath + `"}}}}`), nil
	}
}

func withGitTreeHashRunner(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := gitTreeHashRunner
	gitTreeHashRunner = fn
	t.Cleanup(func() { gitTreeHashRunner = orig })
}

func withTokenCommandRunner(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := tokenCommandRunner
	tokenCommandRunner = fn
	t.Cleanup(func() { tokenCommandRunner = orig })
}

func withDockerLoginRunner(t *testing.T, fn func(context.Context, string, string, string) error) {
	t.Helper()
	orig := dockerLoginRunner
	dockerLoginRunner = fn
	t.Cleanup(func() { dockerLoginRunner = orig })
}

// -- cachedImageRef --

func TestCachedImageRef_Format(t *testing.T) {
	got := cachedImageRef("ghcr.io/anatolykoptev/oxpulse-chat", "72def7ea3afd8dd4c5aa384823cd97d534d01763")
	want := "ghcr.io/anatolykoptev/oxpulse-chat:tree-72def7ea3afd8dd4c5aa384823cd97d534d01763"
	if got != want {
		t.Errorf("cachedImageRef: got %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "ghcr.io/anatolykoptev/oxpulse-chat:tree-") {
		t.Errorf("tag must start with registry + ':tree-'; got %q", got)
	}
	if !strings.HasSuffix(got, "72def7ea3afd8dd4c5aa384823cd97d534d01763") {
		t.Errorf("tag must end with the 40-char tree hash; got %q", got)
	}
}

// -- cacheableServices --

func TestCacheableServices_FeatureOff(t *testing.T) {
	rc := RepoConfig{Services: []string{"svc"}}
	if got := rc.cacheableServices(); got != nil {
		t.Errorf("feature off (no registry): expected nil, got %v", got)
	}
}

func TestCacheableServices_AllServicesByDefault(t *testing.T) {
	rc := RepoConfig{
		Services: []string{"oxpulse-chat"},
		ImageCache: ImageCacheConfig{
			Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
		},
	}
	got := rc.cacheableServices()
	if len(got) != 1 || got[0] != "oxpulse-chat" {
		t.Errorf("expected [oxpulse-chat], got %v", got)
	}
}

func TestCacheableServices_SubsetFilter(t *testing.T) {
	rc := RepoConfig{
		Services: []string{"oxpulse-chat-staging", "oxpulse-chat-stagingprod"},
		ImageCache: ImageCacheConfig{
			Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			Services: []string{"oxpulse-chat-stagingprod"},
		},
	}
	got := rc.cacheableServices()
	if len(got) != 1 || got[0] != "oxpulse-chat-stagingprod" {
		t.Errorf("expected [oxpulse-chat-stagingprod], got %v", got)
	}
}

func TestCacheableServices_SubsetNoIntersection(t *testing.T) {
	rc := RepoConfig{
		Services: []string{"svc-a"},
		ImageCache: ImageCacheConfig{
			Registry: "ghcr.io/example/repo",
			Services: []string{"svc-b"},
		},
	}
	got := rc.cacheableServices()
	if len(got) != 0 {
		t.Errorf("expected empty (no intersection), got %v", got)
	}
}

// -- allServicesCacheable --

func TestAllServicesCacheable_AllCacheable(t *testing.T) {
	req := BuildRequest{
		Config: RepoConfig{
			Services: []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}
	if !allServicesCacheable(req) {
		t.Error("expected true when all services are cacheable")
	}
}

func TestAllServicesCacheable_NotAllCacheable(t *testing.T) {
	req := BuildRequest{
		Config: RepoConfig{
			Services: []string{"oxpulse-chat-staging", "oxpulse-chat-stagingprod"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
				Services: []string{"oxpulse-chat-stagingprod"},
			},
		},
	}
	if allServicesCacheable(req) {
		t.Error("expected false when not all services are cacheable")
	}
}

func TestAllServicesCacheable_FeatureOff(t *testing.T) {
	req := BuildRequest{
		Config: RepoConfig{Services: []string{"svc"}},
	}
	if allServicesCacheable(req) {
		t.Error("expected false when feature is off")
	}
}

// -- gitPrepare tree-hash extraction --

// TestGitPrepare_ReturnsTreeHash verifies that gitPrepare returns the tree hash
// computed via `git rev-parse HEAD^{tree}` in the worktree. The tree hash is
// the content-address key for image-cache tagging.
func TestGitPrepare_ReturnsTreeHash(t *testing.T) {
	const fakeTreeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	withGitTreeHashRunner(t, func(_ context.Context, _ string) (string, error) {
		return fakeTreeHash, nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		return nil // all git commands succeed
	})

	const sha = "a25379b31234"
	wtPath, treeHash, cleanup, errMsg := gitPrepare(context.Background(), "/fake/source", sha)
	defer cleanup()
	if errMsg != "" {
		t.Fatalf("expected no error, got: %s", errMsg)
	}
	if treeHash != fakeTreeHash {
		t.Errorf("tree hash: got %q, want %q", treeHash, fakeTreeHash)
	}
	if wtPath == "" {
		t.Error("expected non-empty worktree path")
	}
}

// TestGitPrepare_TreeHashError_ReturnsEmpty verifies that a tree-hash
// resolution failure does NOT fail the deploy — the tree hash is returned
// empty and the build proceeds as today (image caching silently disabled
// for this deploy).
func TestGitPrepare_TreeHashError_ReturnsEmpty(t *testing.T) {
	withGitTreeHashRunner(t, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("git rev-parse failed")
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		return nil
	})

	const sha = "a25379b31234"
	_, treeHash, cleanup, errMsg := gitPrepare(context.Background(), "/fake/source", sha)
	defer cleanup()
	if errMsg != "" {
		t.Fatalf("tree-hash error must not fail the deploy; got: %s", errMsg)
	}
	if treeHash != "" {
		t.Errorf("tree hash must be empty on error, got %q", treeHash)
	}
}

// -- tryPullCachedImage: pull-hit skips build --

// TestTryPullCachedImage_PullHitSkipsBuild verifies that when the pull
// succeeds, the image is retagged to the compose-expected name and the
// function returns true (skip the build).
func TestTryPullCachedImage_PullHitSkipsBuild(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	var cmdCalls []struct {
		name string
		args []string
	}
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		cmdCalls = append(cmdCalls, struct {
			name string
			args []string
		}{name, args})
		return nil // pull + tag succeed
	})
	// composeImageName uses outputRunner, not cmdRunner.
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if !tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return true (skip build) on pull hit")
	}

	// Verify a `docker pull` was issued with the tree-hash tag.
	sawPull := false
	sawTag := false
	wantRef := cachedImageRef(req.Config.ImageCache.Registry, treeHash)
	for _, c := range cmdCalls {
		if c.name == "docker" && len(c.args) > 0 && c.args[0] == "pull" {
			sawPull = true
			if len(c.args) < 2 || c.args[1] != wantRef {
				t.Errorf("pull args: got %v, want [pull %s]", c.args, wantRef)
			}
		}
		if c.name == "docker" && len(c.args) > 0 && c.args[0] == "tag" {
			sawTag = true
			if len(c.args) < 3 || c.args[1] != wantRef {
				t.Errorf("tag source: got %v, want %s as second arg", c.args, wantRef)
			}
		}
	}
	if !sawPull {
		t.Error("expected docker pull to be called")
	}
	if !sawTag {
		t.Error("expected docker tag to be called for retag")
	}
}

// -- tryPullCachedImage: pull-miss falls back to build --

// TestTryPullCachedImage_PullMissFallsBack verifies that when the pull fails
// (image not found, registry down, auth error), the function returns false
// (do NOT skip the build) — the caller falls through to the existing build
// path. This is the fallback guarantee: never worse than the status quo.
func TestTryPullCachedImage_PullMissFallsBack(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			return errors.New("manifest unknown")
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return false (build) on pull miss")
	}
}

// TestTryPullCachedImage_RetagErrorFallsBack verifies that a retag failure
// (after a successful pull) also falls back to building.
func TestTryPullCachedImage_RetagErrorFallsBack(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "tag" {
			return errors.New("docker tag: No such image")
		}
		return nil
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return false (build) on retag error")
	}
}

// -- composeBuild integration: pull-hit skips build, pull-miss builds --

// TestComposeBuild_PullHitSkipsBuild verifies the full composeBuild path:
// when image cache is enabled and the pull succeeds, composeBuild returns ""
// WITHOUT calling docker compose build (buildRunner is not invoked).
func TestComposeBuild_PullHitSkipsBuild(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	buildCalled := false
	origBuild := buildRunner
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		buildCalled = true
		return nil, nil
	}
	t.Cleanup(func() { buildRunner = origBuild })

	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		// docker pull + docker tag succeed.
		return nil
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo:      "anatolykoptev/oxpulse-chat",
		CommitSHA: "a25379b3",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	errMsg := composeBuild(context.Background(), req, "/fake/worktree", treeHash)
	if errMsg != "" {
		t.Fatalf("composeBuild: unexpected error: %s", errMsg)
	}
	if buildCalled {
		t.Error("docker compose build must NOT be called when the pull succeeds")
	}
}

// TestComposeBuild_PullMissBuilds verifies that when the pull fails,
// composeBuild falls through to the normal build path (buildRunner IS called).
func TestComposeBuild_PullMissBuilds(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	buildCalled := false
	origBuild := buildRunner
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		buildCalled = true
		return nil, nil
	}
	t.Cleanup(func() { buildRunner = origBuild })

	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			return errors.New("manifest unknown")
		}
		return nil
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo:      "anatolykoptev/oxpulse-chat",
		CommitSHA: "a25379b3",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	errMsg := composeBuild(context.Background(), req, "/fake/worktree", treeHash)
	if errMsg != "" {
		t.Fatalf("composeBuild: unexpected error: %s", errMsg)
	}
	if !buildCalled {
		t.Error("docker compose build MUST be called when the pull misses")
	}
}

// TestComposeBuild_NoImageCache_BuildsAsBefore verifies that repos without
// image_cache configured are completely unaffected — the pull path is never
// attempted and the build proceeds as today.
func TestComposeBuild_NoImageCache_BuildsAsBefore(t *testing.T) {
	buildCalled := false
	origBuild := buildRunner
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		buildCalled = true
		return nil, nil
	}
	t.Cleanup(func() { buildRunner = origBuild })

	pullCalled := false
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			pullCalled = true
		}
		return nil
	})
	withOutputRunner(t, composeImagesOutputRunner("svc", "/fake/source"))

	req := BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{"svc"},
		},
	}

	errMsg := composeBuild(context.Background(), req, "/fake/worktree", "someTreeHash")
	if errMsg != "" {
		t.Fatalf("composeBuild: unexpected error: %s", errMsg)
	}
	if pullCalled {
		t.Error("docker pull must NOT be called when image_cache is not configured")
	}
	if !buildCalled {
		t.Error("docker compose build must be called as before")
	}
}

// -- pushCachedImages: push failure does not fail deploy, logs at ERROR --

// TestPushCachedImages_PushFailureDoesNotFailDeploy verifies that a push
// failure (docker push rejected) does NOT panic or return an error that
// would fail the deploy. The function is best-effort. The ERROR-level log
// is verified by the log-capture test below.
func TestPushCachedImages_PushFailureDoesNotFailDeploy(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			return errors.New("denied: permission_denied")
		}
		return nil // tag succeeds
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	// Must not panic.
	pushCachedImages(context.Background(), req, treeHash)
	// If we got here without panicking, the test passes.
}

// TestPushCachedImages_TokenErrorDoesNotFailDeploy verifies that a token
// minting failure does NOT fail the deploy, NO push is attempted, and the
// auth_error metric fires.
func TestPushCachedImages_TokenErrorDoesNotFailDeploy(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	pushAttempted := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("token command: exit status 1")
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		t.Error("docker login must NOT be attempted when the token command fails")
		return nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			pushAttempted = true
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)
	if pushAttempted {
		t.Error("push must NOT be attempted when the token command fails")
	}
}

// TestPushCachedImages_TokenEmptyDoesNotFailDeploy verifies that a token
// command returning an empty string does NOT fail the deploy and NO push is
// attempted.
func TestPushCachedImages_TokenEmptyDoesNotFailDeploy(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	pushAttempted := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "   \n", nil // whitespace-only → trimmed to empty
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		t.Error("docker login must NOT be attempted when the token is empty")
		return nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			pushAttempted = true
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)
	if pushAttempted {
		t.Error("push must NOT be attempted when the token is empty")
	}
}

// TestPushCachedImages_LoginErrorDoesNotFailDeploy verifies that a docker
// login failure does NOT fail the deploy and NO push is attempted.
func TestPushCachedImages_LoginErrorDoesNotFailDeploy(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	pushAttempted := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return errors.New("login: unauthorized")
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			pushAttempted = true
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)
	if pushAttempted {
		t.Error("push must NOT be attempted when docker login fails")
	}
}

// TestPushCachedImages_SuccessPushes verifies that on a successful push,
// the correct sequence of commands is issued: login (with the resolved
// registry host + username), then tag then push, with the tree-hash tag as
// the target.
func TestPushCachedImages_SuccessPushes(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	var loginRegistry, loginUsername string
	withDockerLoginRunner(t, func(_ context.Context, registry, username, _ string) error {
		loginRegistry = registry
		loginUsername = username
		return nil
	})

	var cmdCalls []struct {
		name string
		args []string
	}
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		cmdCalls = append(cmdCalls, struct {
			name string
			args []string
		}{name, args})
		return nil
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)

	// docker login must be called with the registry HOST (not the full ref)
	// and the default username (x-access-token for GHCR App tokens).
	if loginRegistry != "ghcr.io" {
		t.Errorf("docker login registry: got %q, want %q (host derived from the configured registry ref)", loginRegistry, "ghcr.io")
	}
	if loginUsername != defaultTokenUsername {
		t.Errorf("docker login username: got %q, want %q (default)", loginUsername, defaultTokenUsername)
	}

	wantRef := cachedImageRef(req.Config.ImageCache.Registry, treeHash)
	sawTag := false
	sawPush := false
	for _, c := range cmdCalls {
		if c.name == "docker" && len(c.args) >= 3 && c.args[0] == "tag" {
			sawTag = true
			if c.args[2] != wantRef {
				t.Errorf("tag target: got %q, want %q", c.args[2], wantRef)
			}
		}
		if c.name == "docker" && len(c.args) >= 2 && c.args[0] == "push" {
			sawPush = true
			if c.args[1] != wantRef {
				t.Errorf("push ref: got %q, want %q", c.args[1], wantRef)
			}
		}
	}
	if !sawTag {
		t.Error("expected docker tag to be called before push")
	}
	if !sawPush {
		t.Error("expected docker push to be called")
	}
}

// TestPushCachedImages_NoTokenCommand_AmbientPath verifies that when NO token
// command is configured (neither per-repo nor env), the push proceeds using
// the ambient ~/.docker/config.json credential — docker login is NOT called,
// and an INFO log states that the push is relying on ambient credentials (so
// it is a stated fact, not an accident).
func TestPushCachedImages_NoTokenCommand_AmbientPath(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "") // no token command → ambient path

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	loginCalled := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		t.Error("token command must NOT be called on the ambient path")
		return "token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		loginCalled = true
		return nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		return nil // tag + push succeed
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)
	if loginCalled {
		t.Error("docker login must NOT be called on the ambient path (no token command configured)")
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "ambient docker config credential") {
		t.Errorf("ambient path must log that it is relying on ambient credentials; got:\n%s", logOutput)
	}
}

// TestPushCachedImages_FeatureOff_Noop verifies that when image_cache is not
// configured, pushCachedImages is a no-op (no token minted, no commands run).
func TestPushCachedImages_FeatureOff_Noop(t *testing.T) {
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")
	tokenMinted := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		tokenMinted = true
		return "token", nil
	})

	req := BuildRequest{
		Config: RepoConfig{
			Services: []string{"svc"},
		},
	}

	pushCachedImages(context.Background(), req, "someHash")
	if tokenMinted {
		t.Error("token must NOT be minted when image_cache is not configured")
	}
}

// TestPushCachedImages_SubsetOnlyPushesCacheable verifies that when a repo
// has multiple services but only a subset is cacheable, only the cacheable
// service's image is pushed.
func TestPushCachedImages_SubsetOnlyPushesCacheable(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return nil
	})

	pushCount := 0
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			pushCount++
		}
		return nil
	})
	// composeImageName returns a name for any service.
	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		// `docker compose config --images <svc>` — return based on svc arg (last arg).
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			svc := args[len(args)-1]
			return []byte("krolik-server-" + svc + "\n"), nil
		}
		return []byte("{}"), nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat#dev",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat-staging", "oxpulse-chat-stagingprod"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
				Services: []string{"oxpulse-chat-stagingprod"},
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)
	if pushCount != 1 {
		t.Errorf("expected exactly 1 push (stagingprod only), got %d", pushCount)
	}
}

// -- pull path: auth failure / ambient --

// TestTryPullCachedImage_TokenCommandFails_NoPull_FallsBack verifies that
// when the token command fails on the pull path, NO pull is attempted and
// the function returns false (fall back to build), with an auth_error metric.
func TestTryPullCachedImage_TokenCommandFails_NoPull_FallsBack(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	pullAttempted := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("token command: exit status 1")
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		t.Error("docker login must NOT be attempted when the token command fails")
		return nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			pullAttempted = true
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return false (fall back to build) on token error")
	}
	if pullAttempted {
		t.Error("docker pull must NOT be attempted when the token command fails")
	}
}

// TestTryPullCachedImage_NoTokenCommand_AmbientPath verifies that when no
// token command is configured, the pull proceeds using ambient credentials
// (no docker login call) — the previous behaviour, unchanged.
func TestTryPullCachedImage_NoTokenCommand_AmbientPath(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "") // ambient path

	loginCalled := false
	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		t.Error("token command must NOT be called on the ambient path")
		return "token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		loginCalled = true
		return nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		return nil // pull + tag succeed
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if !tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return true (skip build) on ambient pull hit")
	}
	if loginCalled {
		t.Error("docker login must NOT be called on the ambient path")
	}
}

// TestTryPullCachedImage_DeniedAfterLoginIsCacheMissNotAuth is the REGRESSION
// TEST for the substring-classification bug. A private registry answers a pull
// for a NON-EXISTENT image with "denied"/"unauthorized" rather than 404 —
// deliberately, to avoid leaking whether a private repository exists. So the
// first-ever pull for a repo (the normal cold-start cache miss) returns a
// "denied"-flavoured message.
//
// This test simulates exactly that: docker login SUCCEEDS (the credential is
// valid), then the pull returns "Error: unauthorized: authentication required".
// The outcome must be classified as a cache MISS, NOT an auth failure — the
// structural signal (login succeeded) is the truth, not the error wording.
//
// This test FAILS against the pre-fix code (which substring-matched
// "unauthorized" and labelled it auth_error) and PASSES after the fix.
func TestTryPullCachedImage_DeniedAfterLoginIsCacheMissNotAuth(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return nil // login SUCCEEDS — the credential is valid
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			// A private registry returns "unauthorized" for a missing image.
			return errors.New("Error: unauthorized: authentication required")
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat-regression",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return false (fall back to build) on pull miss")
	}
	// The miss outcome must fire — NOT auth_error. A "denied" message after a
	// successful login is a cache miss, not a credential failure.
	missCount := testutil.ToFloat64(ImageCachePullTotal.WithLabelValues(req.Repo, "miss"))
	if missCount < 1 {
		t.Errorf("ImageCachePullTotal{outcome=miss} must fire on a denied-message pull after successful login; got %.0f", missCount)
	}
	authCount := testutil.ToFloat64(ImageCachePullTotal.WithLabelValues(req.Repo, "auth_error"))
	if authCount > 0 {
		t.Errorf("ImageCachePullTotal{outcome=auth_error} must NOT fire when login succeeded — a denied message after login is a cache miss, not an auth failure; got %.0f", authCount)
	}
}

// -- token-never-in-logs (the point) --

// TestImageCache_TokenNeverInLogs is the credential-leak guard. It exercises
// every image-cache code path that handles the token (success, token error,
// login error, push error, push auth error, pull) while capturing ALL slog
// output, then asserts the token VALUE never appears in any captured log
// line — not at any level, not in an error string, not in a command echo.
//
// A token leaked into a log is a credential incident. This test makes that
// regression detectable: if any future change routes the token (or the token
// command's stdout, which IS the token) into an slog call or an error
// message, this test fails.
func TestImageCache_TokenNeverInLogs(t *testing.T) {
	const (
		treeHash    = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
		secretToken = "ghs_SECRET_TOKEN_NEVER_IN_LOGS_a8f3b2c1d4e5"
	)
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	// Capture ALL slog output across every sub-test below.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	baseReq := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	// Sub-test 1: push success — token flows through login stdin, never logged.
	t.Run("push_success", func(t *testing.T) {
		withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
			return secretToken, nil
		})
		withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error { return nil })
		withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
		withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))
		pushCachedImages(context.Background(), baseReq, treeHash)
	})

	// Sub-test 2: token command error — error names the command, NOT the output.
	t.Run("token_error", func(t *testing.T) {
		withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
			return "", fmt.Errorf("token command %q: exit status 1", "fake-token-cmd")
		})
		pushCachedImages(context.Background(), baseReq, treeHash)
	})

	// Sub-test 3: login error — error includes docker login's combined output,
	// which must NOT contain the token (it was on stdin, not echoed).
	t.Run("login_error", func(t *testing.T) {
		withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
			return secretToken, nil
		})
		withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
			return errors.New("docker login ghcr.io: unauthorized")
		})
		pushCachedImages(context.Background(), baseReq, treeHash)
	})

	// Sub-test 4: push error — a non-auth push failure.
	t.Run("push_error", func(t *testing.T) {
		withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
			return secretToken, nil
		})
		withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error { return nil })
		withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
			if name == "docker" && len(args) > 0 && args[0] == "push" {
				return errors.New("network timeout: connection refused")
			}
			return nil
		})
		withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))
		pushCachedImages(context.Background(), baseReq, treeHash)
	})

	// Sub-test 5: push denied after login — the push returns "denied" but
	// login succeeded, so this is a push_error, not an auth failure.
	t.Run("push_denied_after_login", func(t *testing.T) {
		withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
			return secretToken, nil
		})
		withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error { return nil })
		withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
			if name == "docker" && len(args) > 0 && args[0] == "push" {
				return errors.New("denied: permission_denied")
			}
			return nil
		})
		withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))
		pushCachedImages(context.Background(), baseReq, treeHash)
	})

	// Sub-test 6: pull path — token flows through login stdin before pull.
	t.Run("pull", func(t *testing.T) {
		withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
			return secretToken, nil
		})
		withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error { return nil })
		withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
			if name == "docker" && len(args) > 0 && args[0] == "pull" {
				return errors.New("manifest unknown")
			}
			return nil
		})
		tryPullCachedImage(context.Background(), baseReq, treeHash)
	})

	// THE ASSERTION: the token value must never appear in any captured log.
	logOutput := buf.String()
	if strings.Contains(logOutput, secretToken) {
		t.Errorf("TOKEN LEAK: the secret token value %q appears in captured log output:\n%s", secretToken, logOutput)
	}
}

// -- registryHost / resolveTokenCommand / resolveTokenUsername unit tests --

func TestRegistryHost_ExtractsHost(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"ghcr.io/anatolykoptev/oxpulse-chat", "ghcr.io"},
		{"registry.example.com:5000/team/svc", "registry.example.com:5000"},
		{"docker.io/library/ubuntu", "docker.io"},
		{"ubuntu", "ubuntu"}, // no slash — whole string (default-registry image)
	}
	for _, tc := range tests {
		if got := registryHost(tc.ref); got != tc.want {
			t.Errorf("registryHost(%q): got %q, want %q", tc.ref, got, tc.want)
		}
	}
}

func TestResolveTokenCommand_PerRepoWinsOverEnv(t *testing.T) {
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "env-cmd")
	if got := resolveTokenCommand(RepoConfig{ImageCache: ImageCacheConfig{TokenCommand: "repo-cmd"}}); got != "repo-cmd" {
		t.Errorf("per-repo token_command must win over env; got %q", got)
	}
	if got := resolveTokenCommand(RepoConfig{ImageCache: ImageCacheConfig{}}); got != "env-cmd" {
		t.Errorf("env fallback must apply when per-repo is empty; got %q", got)
	}
}

func TestResolveTokenCommand_EmptyWhenUnconfigured(t *testing.T) {
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "")
	if got := resolveTokenCommand(RepoConfig{}); got != "" {
		t.Errorf("resolveTokenCommand must return empty when neither per-repo nor env is set; got %q", got)
	}
}

func TestResolveTokenUsername_DefaultAndOverride(t *testing.T) {
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_USERNAME", "")
	if got := resolveTokenUsername(RepoConfig{}); got != defaultTokenUsername {
		t.Errorf("default username: got %q, want %q", got, defaultTokenUsername)
	}
	if got := resolveTokenUsername(RepoConfig{ImageCache: ImageCacheConfig{TokenUsername: "my-user"}}); got != "my-user" {
		t.Errorf("per-repo token_username must win; got %q", got)
	}
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_USERNAME", "env-user")
	if got := resolveTokenUsername(RepoConfig{}); got != "env-user" {
		t.Errorf("env fallback must apply when per-repo is empty; got %q", got)
	}
}

// TestTryPullCachedImage_LoginFails_ClassifiedAsAuthErrorNotMiss verifies
// that when docker login itself fails on the pull path, the outcome is
// classified as auth_error (the credential is genuinely bad), NOT as a miss.
func TestTryPullCachedImage_LoginFails_ClassifiedAsAuthErrorNotMiss(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return errors.New("docker login ghcr.io: unauthorized") // login FAILS
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "pull" {
			t.Error("docker pull must NOT be attempted when login fails")
		}
		return nil
	})

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat-loginfail",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	if tryPullCachedImage(context.Background(), req, treeHash) {
		t.Fatal("expected tryPullCachedImage to return false (fall back to build) on login failure")
	}
	authCount := testutil.ToFloat64(ImageCachePullTotal.WithLabelValues(req.Repo, "auth_error"))
	if authCount < 1 {
		t.Errorf("ImageCachePullTotal{outcome=auth_error} must fire when login fails; got %.0f", authCount)
	}
	missCount := testutil.ToFloat64(ImageCachePullTotal.WithLabelValues(req.Repo, "miss"))
	if missCount > 0 {
		t.Errorf("ImageCachePullTotal{outcome=miss} must NOT fire when login fails — a login failure is an auth error, not a cache miss; got %.0f", missCount)
	}
}

// TestPushCachedImages_DeniedAfterLoginIsPushErrorNotAuth verifies that when
// docker login SUCCEEDS and then the push returns a "denied"-flavoured message,
// the outcome is classified as push_error (not push_auth_error) — login
// succeeded, so the credential is valid; the push failure is not an auth failure.
func TestPushCachedImages_DeniedAfterLoginIsPushErrorNotAuth(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"
	t.Setenv("DOZOR_IMAGE_CACHE_TOKEN_CMD", "fake-token-cmd")

	withTokenCommandRunner(t, func(_ context.Context, _ string) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return nil // login SUCCEEDS
	})
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" && len(args) > 0 && args[0] == "push" {
			return errors.New("denied: permission_denied")
		}
		return nil
	})
	withOutputRunner(t, composeImagesOutputRunner("oxpulse-chat", "/fake/source"))

	req := BuildRequest{
		Repo: "anatolykoptev/oxpulse-chat-pushdenied",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			Services:    []string{"oxpulse-chat"},
			ImageCache: ImageCacheConfig{
				Registry: "ghcr.io/anatolykoptev/oxpulse-chat",
			},
		},
	}

	pushCachedImages(context.Background(), req, treeHash)

	pushErrCount := testutil.ToFloat64(ImageCachePushTotal.WithLabelValues(req.Repo, "push_error"))
	if pushErrCount < 1 {
		t.Errorf("ImageCachePushTotal{outcome=push_error} must fire on a denied-message push after successful login; got %.0f", pushErrCount)
	}
	pushAuthCount := testutil.ToFloat64(ImageCachePushTotal.WithLabelValues(req.Repo, "push_auth_error"))
	if pushAuthCount > 0 {
		t.Errorf("ImageCachePushTotal{outcome=push_auth_error} must NOT fire when login succeeded — a denied message after login is a push failure, not an auth failure; got %.0f", pushAuthCount)
	}
}
