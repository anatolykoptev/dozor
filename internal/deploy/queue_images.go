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

// snapshotBuiltImages returns a map of service → image ID resolved through
// the service's image NAME (`compose config --format json` → `docker image
// inspect`), never through the project's containers. Used for the
// before/after `compose build` diff in verify-build. Deploy serialization
// (queue + cross-lane lock) makes the tag a stable read between the two
// snapshots — a concurrent retag would need a bypass of the deploy queue.
//
// Why not `docker compose images` here: that command is documented as
// "list images used by the CREATED CONTAINERS" — it reports the image the
// running container was created from. Between `compose build` and
// `compose up` the container still runs the PREVIOUS image, so a
// container-oriented before/after snapshot is identical by construction and
// the no-new-image check false-fires on every real build with a live
// container (issue #214). The tag the build just wrote is the
// container-independent source of truth for "what the build produced".
func snapshotBuiltImages(ctx context.Context, composePath string, services []string) map[string]string {
	ids := make(map[string]string, len(services))
	for _, svc := range services {
		ids[svc] = builtImageID(ctx, composePath, svc)
	}
	return ids
}

// builtImageID returns the image ID the service's resolved image name
// currently points at — i.e. the artifact `compose build` just tagged —
// without consulting any container. Returns "" when the name cannot be
// resolved from the compose model or the ref is absent locally; callers
// treat "" as "cannot compare" (same contract as snapshotImages).
func builtImageID(ctx context.Context, composePath, svc string) string {
	name := composeImageName(ctx, composePath, svc)
	if name == "" {
		// `config --format json` resolves a name for every defined service
		// (explicit `image:` or the <project>-<svc> default), so "" here
		// means the service is missing from the compose model or the config
		// itself failed — never a normal state; surface it rather than
		// silently disabling the diff for this service.
		slog.Warn("deploy: cannot resolve image name for service", "service", svc)
		return ""
	}
	id := imageIDForRef(ctx, composePath, name)
	if id == "" {
		// The compose model resolved a name but no local image carries it.
		// Pre-build that is a normal cold start; post-build it means the
		// build tagged something else — divergent enough to be worth a
		// warning rather than a silent "".
		slog.Warn("deploy: resolved image name has no local image",
			"service", svc, "ref", name)
	}
	return id
}

// imageIDForRef returns the `docker image inspect` .Id a ref resolves to.
// "" when the ref is absent or inspect fails. The ref comes from
// composeImageName (own compose file, validated by looksLikeImageRef).
func imageIDForRef(ctx context.Context, dir, ref string) string {
	out, err := outputRunner(ctx, dir,
		"docker", "image", "inspect", "--format", "{{.Id}}", ref) //nolint:gosec // trusted local config, not shell
	if err != nil {
		return ""
	}
	// `inspect --format {{.Id}}` always yields sha256:<hex> on success; treat
	// anything else (template drift, plugin output) as unresolvable rather
	// than comparing garbage strings as image identities.
	id := strings.TrimSpace(string(out))
	if !strings.HasPrefix(id, "sha256:") {
		return ""
	}
	return id
}

// snapshotImages returns a map of service → image ID for the given services,
// read from the project's CURRENT containers (`docker compose images`).
// Container-oriented by design: its only caller is the rollback path, which
// needs the image the running container was actually started from — NOT the
// fresh tag a just-finished build may already point at (post-build/pre-up,
// tag resolution would capture the NEW image and rollback would retag it
// over itself). Never use this for build-diff verification.
func snapshotImages(ctx context.Context, composePath string, services []string) map[string]string {
	ids := make(map[string]string, len(services))
	for _, svc := range services {
		ids[svc] = composeImageID(ctx, composePath, svc)
	}
	return ids
}

// composeImageID resolves the image ID of the service's CURRENT container.
// Uses `docker compose images`, which lists images used by created
// containers — it does NOT report the freshly built tag (the container keeps
// reporting the old image until `compose up` recreates it). Correct for the
// rollback path (restore what the container ran); wrong for build-diff
// verification (use builtImageID). Returns "" if there is no container or
// the command fails.
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
// Resolution: ONE `docker compose config --format json` render, then
// per-service from the JSON model (serviceImageName). The previous
// implementation ran `config --images <svc>` and took the first
// valid-looking line — but a service argument there does NOT isolate the
// service: compose renders the service WITH its depends_on tree and prints
// one image per line in nondeterministic order, so the first line is
// frequently a DEPENDENCY's image (issue #240: the build-diff then compared
// e.g. ox-browser's unchanged image ID before and after a go-wowa rebuild
// and failed the deploy; on the cache path it would have retagged/pushed a
// dependency's artifact under this repo's tree-hash tag — a wrong-artifact
// publish). The JSON model carries each service keyed by name, so the
// dependency tree cannot bleed into this lookup.
func composeImageName(ctx context.Context, composePath, svc string) string {
	out, err := outputRunner(ctx, composePath,
		"docker", "compose", "config", "--format", "json") //nolint:gosec // trusted local config, not shell
	if err != nil {
		return ""
	}
	return serviceImageName(out, svc)
}

// serviceImageName extracts the resolved image name for svc from the JSON
// model emitted by `docker compose config --format json`. Order of
// precedence, mirroring what `compose build` tags:
//  1. explicit `image:` field — used verbatim;
//  2. build-only service (no `image:`) — the compose default
//     `<project>-<service>`, where <project> is the model's resolved
//     top-level `name` (COMPOSE_PROJECT_NAME / name: / dir-basename already
//     applied by compose);
//  3. otherwise "" — fail loudly; never guess.
func serviceImageName(configJSON []byte, svc string) string {
	var cfg struct {
		Name     string `json:"name"`
		Services map[string]struct {
			Image string          `json:"image"`
			Build json.RawMessage `json:"build"`
		} `json:"services"`
	}
	if json.Unmarshal(configJSON, &cfg) != nil {
		return ""
	}
	svcCfg, ok := cfg.Services[svc]
	if !ok {
		return ""
	}
	if name := strings.TrimSpace(svcCfg.Image); looksLikeImageRef(name) {
		return name
	}
	// `build` is present whenever the service builds (normalized to an
	// object in the JSON model); a missing or null build means a pull-only
	// service with no image — nothing sane to return.
	if len(svcCfg.Build) == 0 || string(svcCfg.Build) == "null" || cfg.Name == "" {
		return ""
	}
	if name := cfg.Name + "-" + svc; looksLikeImageRef(name) {
		return name
	}
	return ""
}

// looksLikeImageRef is a minimal guard against malformed `config` model
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
