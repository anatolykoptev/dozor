package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestPruneBuildkitCache_DisabledByDefault verifies that when
// PruneBuildkitCache is false (the default), pruneBuildkitCacheMount is never
// called and composeBuild proceeds without touching buildx prune.
func TestPruneBuildkitCache_DisabledByDefault(t *testing.T) {
	origPrune := pruneRunner
	defer func() { pruneRunner = origPrune }()

	called := false
	pruneRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		called = true
		return nil, nil
	}

	req := BuildRequest{
		CommitSHA: "abc1234",
		Config: RepoConfig{
			PruneBuildkitCache: false,
			ComposePath:        "/tmp",
			Services:           []string{"svc"},
		},
	}

	pruneBuildkitCacheMount(context.Background(), req)

	if called {
		t.Error("expected pruneRunner not to be called when PruneBuildkitCache=false")
	}
}

// TestPruneBuildkitCache_EnabledInvokesBuildxPrune verifies that when
// PruneBuildkitCache is true, pruneBuildkitCacheMount invokes
// `docker buildx prune --force --filter type=exec.cachemount`.
func TestPruneBuildkitCache_EnabledInvokesBuildxPrune(t *testing.T) {
	origPrune := pruneRunner
	defer func() { pruneRunner = origPrune }()

	var capturedArgs []string
	pruneRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("763.2MiB freed"), nil
	}

	req := BuildRequest{
		CommitSHA: "deadbeef",
		Config: RepoConfig{
			PruneBuildkitCache: true,
			ComposePath:        "/fake/compose",
			Services:           []string{"embed-server"},
		},
	}

	pruneBuildkitCacheMount(context.Background(), req)

	if len(capturedArgs) == 0 {
		t.Fatal("expected pruneRunner to be called with args, got none")
	}

	// Must be: docker buildx prune --force --filter type=exec.cachemount
	wantSubcmd := "buildx"
	if capturedArgs[0] != wantSubcmd {
		t.Errorf("expected first arg %q, got %q", wantSubcmd, capturedArgs[0])
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "--force") {
		t.Errorf("expected --force in args, got: %s", joined)
	}
	if !strings.Contains(joined, "type=exec.cachemount") {
		t.Errorf("expected type=exec.cachemount filter in args, got: %s", joined)
	}
}

// TestPruneBuildkitCache_FailureDoesNotBlockBuild verifies that when
// pruneRunner returns an error, pruneBuildkitCacheMount does NOT propagate
// the error — prune is best-effort and must never block a build.
func TestPruneBuildkitCache_FailureDoesNotBlockBuild(t *testing.T) {
	origPrune := pruneRunner
	defer func() { pruneRunner = origPrune }()

	pruneRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("some docker error"), errors.New("exit status 1")
	}

	req := BuildRequest{
		CommitSHA: "cafebabe",
		Config: RepoConfig{
			PruneBuildkitCache: true,
			ComposePath:        "/fake/compose",
			Services:           []string{"embed-server"},
		},
	}

	// Must not panic or return an error — function signature is void.
	pruneBuildkitCacheMount(context.Background(), req)
}
