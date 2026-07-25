package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// snapshotImages returns a map of service → image ID for the given services.
// Used to detect no-op builds (same image ID before/after `compose build`).
func snapshotImages(ctx context.Context, composePath string, services []string) map[string]string {
	ids := make(map[string]string, len(services))
	for _, svc := range services {
		ids[svc] = composeImageID(ctx, composePath, svc)
	}
	return ids
}

// composeImageID resolves the image ID that would be used by `compose up <svc>`.
// Uses `docker compose images` which works for both `image:` and `build:` services
// — `compose config` only returns image refs for services with an explicit image
// field, which is the common mistake. Returns empty string if the image is absent
// or the command fails.
func composeImageID(ctx context.Context, composePath, svc string) string {
	// svc is a service name from our own deploy-repos.yaml (trusted local config),
	// passed as an individual argv slot — not interpolated into a shell.
	cmd := exec.CommandContext(ctx, "docker", "compose", "images", "--format", "json", svc) //nolint:gosec // trusted local config, not shell
	cmd.Dir = composePath
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	// `compose images --format json` returns either a JSON array or a stream of
	// newline-delimited objects depending on the Docker version. Try both shapes.
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "[") {
		return imageIDFromArray(trimmed, svc)
	}
	return imageIDFromNDJSON(trimmed, svc)
}

type imageIDEntry struct {
	ID            string `json:"ID"`
	ContainerName string `json:"ContainerName"`
}

func imageIDFromArray(trimmed, svc string) string {
	var arr []imageIDEntry
	if json.Unmarshal([]byte(trimmed), &arr) != nil {
		return ""
	}
	for _, e := range arr {
		if e.ContainerName == svc || strings.HasSuffix(e.ContainerName, "_"+svc) {
			return e.ID
		}
	}
	if len(arr) == 1 {
		return arr[0].ID
	}
	return ""
}

func imageIDFromNDJSON(trimmed, svc string) string {
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e imageIDEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.ContainerName == svc || strings.HasSuffix(e.ContainerName, "_"+svc) {
			return e.ID
		}
	}
	return ""
}

// composeImageName returns the image name (repo[:tag]) for a service.
//
// It resolves the name from the compose MODEL, never from the project's
// containers. This is load-bearing: the resolver runs at image-cache push time
// (right after `compose build`, BEFORE `compose up` recreates the container)
// and on the pull-before-build path (where no container exists yet). In both
// positions `docker compose images` is container-oriented — it reports the
// images of the project's CURRENT containers, which is either empty (no
// container yet) or the PREVIOUS container's stale image. Pushing that stale
// image under the new tree-hash tag would publish a wrong artifact under a tag
// that claims to be the new tree — a silent wrong-artifact bug.
//
// Invariant: the returned name is the image the build just produced (or will
// produce), resolved from compose config — never "whatever the old container
// happens to run". If no container-independent source can be trusted, return ""
// and fail loudly; never guess, never fall back to a container-oriented source.
//
// Resolution chain (every link is container-independent):
//  1. `docker compose config --images <svc>` — compose v2 prints the resolved
//     image ref (the default `<project>-<svc>` for build-only services, or the
//     explicit `image:` ref) without needing any container.
//  2. `docker compose config --format json` → services[svc].image — only
//     non-empty for services with an explicit `image:` field (the default name
//     for build-only services is computed by compose and is NOT exposed in the
//     JSON model), so this link only helps explicit-image services on compose
//     builds that lack the `--images` flag. Still container-independent.
func composeImageName(ctx context.Context, composePath, svc string) string {
	if name := composeConfigImagesName(ctx, composePath, svc); name != "" {
		return name
	}
	if name := composeConfigJSONImage(ctx, composePath, svc); name != "" {
		return name
	}
	return ""
}

// composeConfigImagesName runs `docker compose config --images <svc>` and
// returns the first line that looks like a valid image reference. Compose v2
// prints one resolved image ref per line; for a single service, one line.
func composeConfigImagesName(ctx context.Context, composePath, svc string) string {
	// svc is a service name from our own deploy-repos.yaml (trusted local
	// config), passed as an individual argv slot — not interpolated into a shell.
	out, err := outputRunner(ctx, composePath,
		"docker", "compose", "config", "--images", svc) //nolint:gosec // trusted local config, not shell
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); looksLikeImageRef(name) {
			return name
		}
	}
	return ""
}

// composeConfigJSONImage parses `docker compose config --format json` and
// returns the explicit `image:` field for the service. Returns "" for
// build-only services (no explicit image) — those are handled by the
// `--images` link above. Container-independent.
func composeConfigJSONImage(ctx context.Context, composePath, svc string) string {
	out, err := outputRunner(ctx, composePath,
		"docker", "compose", "config", "--format", "json")
	if err != nil {
		return ""
	}
	var cfg struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if json.Unmarshal(out, &cfg) != nil {
		return ""
	}
	svcCfg, ok := cfg.Services[svc]
	if !ok {
		return ""
	}
	if name := strings.TrimSpace(svcCfg.Image); looksLikeImageRef(name) {
		return name
	}
	return ""
}

// looksLikeImageRef is a minimal guard against malformed `config --images`
// output producing a bogus tag. It rejects empty strings, internal whitespace,
// and characters not allowed in a Docker image reference. It is intentionally
// permissive (not a full grammar) — the goal is to catch garbage, not to
// validate every legal ref.
func looksLikeImageRef(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '/', r == ':', r == '-', r == '_', r == '@':
		default:
			return false
		}
	}
	return true
}

// rollbackImages attempts to restore services to their previous image IDs.
func rollbackImages(ctx context.Context, composePath string, services []string, previousImages map[string]string) error {
	if len(previousImages) == 0 {
		return errors.New("no previous images to rollback to")
	}
	for _, svc := range services {
		prevID := previousImages[svc]
		if prevID == "" {
			continue
		}
		currentID := composeImageID(ctx, composePath, svc)
		if currentID == prevID {
			slog.Info("deploy: rollback skipped, image unchanged",
				"service", svc, "image", prevID[:7])
			continue
		}
		imgName := composeImageName(ctx, composePath, svc)
		if imgName == "" {
			return fmt.Errorf("rollback %s: cannot determine image name", svc)
		}
		if err := runCmd(ctx, composePath, "docker", "tag", prevID, imgName); err != nil {
			return fmt.Errorf("rollback %s: tag %s as %s: %w", svc, prevID[:7], imgName, err)
		}
		upArgs := []string{"compose", "up", "-d", "--no-deps", "--no-build", "--force-recreate", svc}
		if err := runCmd(ctx, composePath, "docker", upArgs...); err != nil {
			return fmt.Errorf("rollback %s: compose up: %w", svc, err)
		}
		slog.Warn("deploy: rolled back service",
			"service", svc,
			"from", currentID[:7],
			"to", prevID[:7],
		)
	}
	return nil
}
