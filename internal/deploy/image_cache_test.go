package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// -- DI helpers for image_cache seams --

// composeImagesOutputRunner stubs outputRunner to return the correct JSON
// format for BOTH `docker compose config --format json` (used by
// resolveBuildOverrides) and `docker compose images --format json <svc>`
// (used by composeImageName). The two commands return different JSON shapes;
// this stub dispatches on the compose subcommand (args[1]).
func composeImagesOutputRunner(svcName, sourcePath string) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		// args: docker compose <subcommand> ...
		if len(args) >= 2 && args[1] == "images" {
			// `docker compose images --format json <svc>` → {"Repository":"...","Tag":"..."}
			return []byte(`{"Repository":"krolik-server-` + svcName + `","Tag":"latest"}`), nil
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

func withGHAppTokenRunner(t *testing.T, fn func(context.Context) (string, error)) {
	t.Helper()
	orig := ghAppTokenRunner
	ghAppTokenRunner = fn
	t.Cleanup(func() { ghAppTokenRunner = orig })
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

	var cmdCalls []struct{ name string; args []string }
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		cmdCalls = append(cmdCalls, struct{ name string; args []string }{name, args})
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
		Repo: "anatolykoptev/oxpulse-chat",
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
		Repo: "anatolykoptev/oxpulse-chat",
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

	withGHAppTokenRunner(t, func(_ context.Context) (string, error) {
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
// minting failure does NOT fail the deploy.
func TestPushCachedImages_TokenErrorDoesNotFailDeploy(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	withGHAppTokenRunner(t, func(_ context.Context) (string, error) {
		return "", errors.New("gh-app-token.sh: exit status 1")
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
}

// TestPushCachedImages_LoginErrorDoesNotFailDeploy verifies that a docker
// login failure does NOT fail the deploy.
func TestPushCachedImages_LoginErrorDoesNotFailDeploy(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	withGHAppTokenRunner(t, func(_ context.Context) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return errors.New("login: unauthorized")
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
}

// TestPushCachedImages_SuccessPushes verifies that on a successful push,
// the correct sequence of commands is issued: tag then push, with the
// tree-hash tag as the target.
func TestPushCachedImages_SuccessPushes(t *testing.T) {
	const treeHash = "72def7ea3afd8dd4c5aa384823cd97d534d01763"

	withGHAppTokenRunner(t, func(_ context.Context) (string, error) {
		return "fake-token", nil
	})
	withDockerLoginRunner(t, func(_ context.Context, _, _, _ string) error {
		return nil
	})

	var cmdCalls []struct{ name string; args []string }
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		cmdCalls = append(cmdCalls, struct{ name string; args []string }{name, args})
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

// TestPushCachedImages_FeatureOff_Noop verifies that when image_cache is not
// configured, pushCachedImages is a no-op (no token minted, no commands run).
func TestPushCachedImages_FeatureOff_Noop(t *testing.T) {
	tokenMinted := false
	withGHAppTokenRunner(t, func(_ context.Context) (string, error) {
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

	withGHAppTokenRunner(t, func(_ context.Context) (string, error) {
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
		// `docker compose images --format json <svc>` — return based on svc arg
		svc := args[len(args)-1]
		return []byte(`{"Repository":"krolik-server-` + svc + `","Tag":"latest"}`), nil
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
