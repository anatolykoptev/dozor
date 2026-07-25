package deploy

import (
	"context"
	"strings"
	"testing"
)

// withOutputRunnerFn swaps outputRunner for the duration of the test.
func withOutputRunnerFn(t *testing.T, fn func(context.Context, string, string, ...string) ([]byte, error)) {
	t.Helper()
	orig := outputRunner
	outputRunner = fn
	t.Cleanup(func() { outputRunner = orig })
}

// TestComposeImageName_ResolvesViaConfigNotContainers is the RED test for the
// image-cache push failure: composeImageName MUST resolve the image name from
// the compose model (`docker compose config --images <svc>`), NOT from the
// project's containers (`docker compose images`, which is container-oriented).
//
// At push time — right after `compose build`, before `compose up` recreates the
// container — `docker compose images` either returns nothing (no container yet)
// or returns the PREVIOUS container's image. The latter is the dangerous
// variant: it would push a stale artifact under the new tree-hash tag. This
// test proves the resolver never returns the stale container image: even when
// `docker compose images` reports a stale image, composeImageName returns the
// config-resolved name the build just produced.
func TestComposeImageName_ResolvesViaConfigNotContainers(t *testing.T) {
	const staleRepo = "old-previous-image"
	const staleTag = "vold"
	const freshName = "krolik-server-oxpulse-chat-stagingprod"

	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		// `docker compose images --format json <svc>` — container-oriented,
		// reports the PREVIOUS container's (stale) image. Must be ignored.
		if len(args) >= 2 && args[1] == "images" {
			return []byte(`[{"Repository":"` + staleRepo + `","Tag":"` + staleTag + `","ContainerName":"oxpulse-chat-stagingprod"}]`), nil
		}
		// `docker compose config --images <svc>` — container-independent,
		// resolves the image name the build just produced.
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			return []byte(freshName + "\n"), nil
		}
		return []byte("{}"), nil
	})

	got := composeImageName(context.Background(), "/fake/compose", "oxpulse-chat-stagingprod")
	if got != freshName {
		t.Errorf("composeImageName: got %q, want %q (config-resolved name, not the stale container image)", got, freshName)
	}
	if strings.Contains(got, staleRepo) {
		t.Errorf("composeImageName returned the STALE container image %q — the resolver must never surface a previous container's image", got)
	}
}

// TestComposeImageName_ConfigImagesEmptyReturnsEmpty verifies that when the
// container-independent source yields nothing (service misconfigured, compose
// parse error), the resolver fails loudly by returning "" — it never falls
// back to a container-oriented source that could return a stale image.
func TestComposeImageName_ConfigImagesEmptyReturnsEmpty(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "images" {
			// A stale container image exists — must NOT be used as a fallback.
			return []byte(`[{"Repository":"stale-fallback","Tag":"latest","ContainerName":"svc"}]`), nil
		}
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			return []byte("\n  \n"), nil // empty / whitespace only
		}
		return []byte("{}"), nil
	})

	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != "" {
		t.Errorf("composeImageName: expected \"\" when config --images is empty, got %q (must fail loudly, not fall back to containers)", got)
	}
}

// TestComposeImageName_GarbageOutputReturnsEmpty verifies that malformed
// `config --images` output (not a valid image reference) does not produce a
// bogus image name — the resolver returns "" rather than pushing under a
// garbage tag.
func TestComposeImageName_GarbageOutputReturnsEmpty(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			return []byte("not a valid image ref!!!\n"), nil
		}
		return []byte("{}"), nil
	})
	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != "" {
		t.Errorf("composeImageName: expected \"\" for garbage output, got %q", got)
	}
}

// TestComposeImageName_CommandErrorReturnsEmpty verifies that a command error
// (e.g. compose binary missing, compose file invalid) results in "" — fail
// loudly, never guess.
func TestComposeImageName_CommandErrorReturnsEmpty(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			return nil, errComposeBoom
		}
		return []byte("{}"), nil
	})
	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != "" {
		t.Errorf("composeImageName: expected \"\" on command error, got %q", got)
	}
}

// TestComposeImageName_ExplicitImageWithTag verifies the resolver returns the
// full repo:tag for a service with an explicit `image:` field (the config
// source includes the tag, unlike the build-only default-name case).
func TestComposeImageName_ExplicitImageWithTag(t *testing.T) {
	const ref = "ghcr.io/anatolykoptev/oxpulse-chat:v1.2.3"
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[1] == "config" && args[2] == "--images" {
			return []byte(ref + "\n"), nil
		}
		return []byte("{}"), nil
	})
	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != ref {
		t.Errorf("composeImageName: got %q, want %q", got, ref)
	}
}

var errComposeBoom = newSentinelErr("compose config: boom")

func newSentinelErr(msg string) error { return &sentinelErr{msg: msg} }

type sentinelErr struct{ msg string }

func (e *sentinelErr) Error() string { return e.msg }
