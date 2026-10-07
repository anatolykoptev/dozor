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
// the compose model (`docker compose config --format json`), NOT from the
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
		// `docker compose config --format json` — container-independent;
		// build-only service resolves to the <project>-<svc> default name.
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"name":"krolik-server","services":{"oxpulse-chat-stagingprod":{"build":{"context":"/x"}}}}`), nil
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

// TestComposeImageName_ConfigEmptyReturnsEmpty verifies that when the
// container-independent source yields nothing (service misconfigured, compose
// parse error), the resolver fails loudly by returning "" — it never falls
// back to a container-oriented source that could return a stale image.
func TestComposeImageName_ConfigEmptyReturnsEmpty(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "images" {
			// A stale container image exists — must NOT be used as a fallback.
			return []byte(`[{"Repository":"stale-fallback","Tag":"latest","ContainerName":"svc"}]`), nil
		}
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"name":"krolik-server","services":{}}`), nil // svc absent
		}
		return []byte("{}"), nil
	})

	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != "" {
		t.Errorf("composeImageName: expected \"\" when the service is absent from the config model, got %q (must fail loudly, not fall back to containers)", got)
	}
}

// TestComposeImageName_GarbageOutputReturnsEmpty verifies that malformed
// `config --format json` output (unparseable, or an image field that is not a
// valid image reference) does not produce a bogus image name — the resolver
// returns "" rather than pushing under a garbage tag.
func TestComposeImageName_GarbageOutputReturnsEmpty(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "config" {
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
		if len(args) >= 2 && args[1] == "config" {
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
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"name":"krolik-server","services":{"svc":{"image":"` + ref + `"}}}`), nil
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

// TestSnapshotBuiltImages_IgnoresRunningContainer is the regression test for
// issue #214: the before/after `compose build` diff must read the image the
// service's TAG resolves to (the artifact the build just wrote), never the
// running container's image — which stays the OLD image until `compose up`
// recreates it. (`compose images` can't even be faked here: the container
// read runs through exec.CommandContext, not the outputRunner seam — the
// tag path is structurally unable to consult it.)
func TestSnapshotBuiltImages_IgnoresRunningContainer(t *testing.T) {
	const freshID = "sha256:fresh000fresh"
	const imgName = "krolik-server-oxpulse-chat-stagingprod:latest"

	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"name":"krolik-server","services":{"oxpulse-chat-stagingprod":{"image":"` + imgName + `"}}}`), nil
		}
		if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
			// the ref must be the config-resolved name, not a guess
			if args[len(args)-1] == imgName {
				return []byte(freshID + "\n"), nil
			}
			return nil, errComposeBoom
		}
		return []byte("{}"), nil
	})

	got := snapshotBuiltImages(context.Background(), "/fake/compose", []string{"oxpulse-chat-stagingprod"})
	if got["oxpulse-chat-stagingprod"] != freshID {
		t.Errorf("snapshotBuiltImages: got %q, want %q (tag-resolved image, not the stale container image)", got["oxpulse-chat-stagingprod"], freshID)
	}
}

// -- Issue #240: `config --images <svc>` emits the DEPENDENCY tree too --
//
// On compose v5 (fleet: v5.1.0), `docker compose config --images go-wowa`
// prints one image line per service for go-wowa AND its whole depends_on
// tree (cloakbrowser, ox-browser), in nondeterministic map order — the
// service's own line is frequently NOT first. Verified live on krolik
// 2026-10-07: 8 consecutive runs returned `krolik-server-ox-browser` first
// 6 times. A resolver that takes the first valid line inspects the
// dependency's image — which never changes on this deploy — so the
// before/after diff reports "no new image" and FAILS the deploy even though
// `compose build` just tagged a fresh image. (go-wowa v0.13.12, v0.13.13,
// go-code 1.72.0 all died this way.)

// TestComposeBuild_ConfigImagesListsDeps_NoFalseFail is the regression test:
// `config --images` returns a dependency first, the service's own image DID
// change across the build — the deploy must NOT fail.
//
// RED-on-current-code: composeImageName takes the first `config --images`
// line (the dependency), both snapshots inspect that dep image → identical
// IDs → logImageDiff fails the deploy.
func TestComposeBuild_ConfigImagesListsDeps_NoFalseFail(t *testing.T) {
	const (
		svcName  = "go-wowa"
		svcImage = "krolik-server-go-wowa"
		depImage = "krolik-server-ox-browser"
		oldID    = "sha256:0d75dd7d2b48aaaa"
		newID    = "sha256:e377e4e7476dbbbb"
		depID    = "sha256:dep000dep000dep0"
	)
	builtRan := false
	var inspectedRefs []string
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[1] == "config" && args[2] == "--images":
			// Real compose v5.1 output shape for `config --images go-wowa`:
			// the service's depends_on tree, dep line first.
			return []byte(depImage + "\n" + svcImage + "\n"), nil
		case len(args) >= 2 && args[1] == "config":
			return []byte(`{"name":"krolik-server","services":{"go-wowa":{"build":{"context":"/home/krolik/src/go-wowa"}},"ox-browser":{"build":{"context":"/home/krolik/src/ox-browser"}}}}`), nil
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			ref := args[len(args)-1]
			inspectedRefs = append(inspectedRefs, ref)
			switch ref {
			case svcImage:
				if builtRan {
					return []byte(newID + "\n"), nil
				}
				return []byte(oldID + "\n"), nil
			case depImage:
				return []byte(depID + "\n"), nil // deps never change on this build
			}
			return nil, errComposeBoom
		}
		return []byte("{}"), nil
	})
	origBuild := buildRunner
	t.Cleanup(func() { buildRunner = origBuild })
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		builtRan = true // the build DID tag a new image for the service
		return nil, nil
	}
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })

	req := BuildRequest{
		Repo:      "anatolykoptev/go-wowa",
		CommitSHA: "9e57d2974426b7e070cb0deadbeefcafe1234567", // valid 40-hex → diff is armed
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{svcName},
		},
	}
	errMsg, builtNow := composeBuild(context.Background(), req, "", "")
	if errMsg != "" {
		t.Fatalf("ISSUE #240 REGRESSION: composeBuild failed a deploy whose image DID change: %s", errMsg)
	}
	if !builtNow {
		t.Error("expected builtNow=true — a build ran and produced a new image")
	}
	for _, ref := range inspectedRefs {
		if ref == depImage {
			t.Errorf("image diff inspected the DEPENDENCY image %q instead of the service's own %q", depImage, svcImage)
		}
	}
}

// TestComposeBuild_GenuinelyUnchangedImage_StillFails is the fail-loud guard:
// when the service's own image ID is identical before and after the build,
// the deploy MUST still fail — the check must not be deleted into oblivion
// while fixing #240 (a genuinely stale cache stays a hard failure).
func TestComposeBuild_GenuinelyUnchangedImage_StillFails(t *testing.T) {
	const (
		svcName  = "go-wowa"
		svcImage = "krolik-server-go-wowa"
		depImage = "krolik-server-ox-browser"
		sameID   = "sha256:same000same000"
		depID    = "sha256:dep000dep000dep0"
	)
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[1] == "config" && args[2] == "--images":
			return []byte(depImage + "\n" + svcImage + "\n"), nil
		case len(args) >= 2 && args[1] == "config":
			return []byte(`{"name":"krolik-server","services":{"go-wowa":{"build":{"context":"/home/krolik/src/go-wowa"}}}}`), nil
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			if args[len(args)-1] == svcImage {
				return []byte(sameID + "\n"), nil // genuinely unchanged
			}
			return []byte(depID + "\n"), nil
		}
		return []byte("{}"), nil
	})
	origBuild := buildRunner
	t.Cleanup(func() { buildRunner = origBuild })
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil // build "succeeds" but produces nothing new (stale cache)
	}
	withShortSHARunnerManual(t, func(_ context.Context, _ string) (string, error) { return "abc1234", nil })

	req := BuildRequest{
		Repo:      "anatolykoptev/go-wowa",
		CommitSHA: "9e57d2974426b7e070cb0deadbeefcafe1234567",
		Config: RepoConfig{
			ComposePath: "/fake/compose",
			SourcePath:  "/fake/source",
			Services:    []string{svcName},
		},
	}
	errMsg, builtNow := composeBuild(context.Background(), req, "", "")
	if !strings.Contains(errMsg, "no new image") {
		t.Fatalf("expected 'no new image' failure for a genuinely unchanged image, got errMsg=%q", errMsg)
	}
	if builtNow {
		t.Error("expected builtNow=false when the build produced no new image")
	}
}

// TestComposeImageName_DepsNoise_ReturnsOwnName is the unit-level pin of the
// same bug: with `config --images` output polluted by depends_on images,
// composeImageName must still return the service's OWN resolved name —
// here the compose-default `<project>-<service>` for a build-only service.
//
// RED-on-current-code: the first-line pick returns the dependency's
// "krolik-server-ox-browser" instead of "krolik-server-go-wowa".
func TestComposeImageName_DepsNoise_ReturnsOwnName(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[1] == "config" && args[2] == "--images":
			return []byte("krolik-server-ox-browser\nkrolik-server-go-wowa\n"), nil
		case len(args) >= 2 && args[1] == "config":
			return []byte(`{"name":"krolik-server","services":{"go-wowa":{"build":{"context":"/src/go-wowa"}}}}`), nil
		}
		return []byte("{}"), nil
	})
	got := composeImageName(context.Background(), "/fake/compose", "go-wowa")
	if got != "krolik-server-go-wowa" {
		t.Errorf("composeImageName: got %q, want %q (the service's own image, never a depends_on sibling's)", got, "krolik-server-go-wowa")
	}
}

// TestComposeImageName_ExplicitImageWinsOverDefault verifies an explicit
// `image:` field is returned verbatim (not replaced by <project>-<svc>).
func TestComposeImageName_ExplicitImageWinsOverDefault(t *testing.T) {
	const ref = "ghcr.io/anatolykoptev/oxpulse-chat:v1.2.3"
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"name":"krolik-server","services":{"svc":{"image":"` + ref + `","build":{"context":"/x"}}}}`), nil
		}
		return []byte("{}"), nil
	})
	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != ref {
		t.Errorf("composeImageName: got %q, want %q", got, ref)
	}
}

// TestComposeImageName_NoProjectNameFailsLoud verifies that a build-only
// service with no resolvable project name yields "" — fail loudly rather
// than guess a tag (a wrong guess silently inspects a foreign image).
func TestComposeImageName_NoProjectNameFailsLoud(t *testing.T) {
	withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "config" {
			return []byte(`{"services":{"svc":{"build":{"context":"/x"}}}}`), nil
		}
		return []byte("{}"), nil
	})
	if got := composeImageName(context.Background(), "/fake/compose", "svc"); got != "" {
		t.Errorf("composeImageName: expected \"\" when project name is unresolvable, got %q", got)
	}
}

// TestBuiltImageID_MissingImageReturnsEmpty verifies that a service whose
// image name cannot be resolved, or whose tag is absent locally, yields ""
// — the "cannot compare" contract the caller's before != "" guard expects.
func TestBuiltImageID_MissingImageReturnsEmpty(t *testing.T) {
	t.Run("name unresolvable", func(t *testing.T) {
		withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[1] == "config" {
				return nil, errComposeBoom
			}
			return []byte("{}"), nil
		})
		if got := builtImageID(context.Background(), "/fake/compose", "svc"); got != "" {
			t.Errorf("builtImageID: expected \"\" when name resolution fails, got %q", got)
		}
	})

	t.Run("tag absent", func(t *testing.T) {
		withOutputRunnerFn(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[1] == "config" {
				return []byte(`{"name":"krolik-server","services":{"svc":{"build":{"context":"/x"}}}}`), nil
			}
			if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
				return nil, errComposeBoom // first deploy: tag never existed
			}
			return []byte("{}"), nil
		})
		if got := builtImageID(context.Background(), "/fake/compose", "svc"); got != "" {
			t.Errorf("builtImageID: expected \"\" for absent tag, got %q", got)
		}
	})
}
