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

// composeImageName returns the image name (repo:tag) for a service.
func composeImageName(ctx context.Context, composePath, svc string) string {
	out, err := outputRunner(ctx, composePath,
		"docker", "compose", "images", "--format", "json", svc)
	if err != nil || len(out) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(out))
	type imgEntry struct {
		Repository string `json:"Repository"`
		Tag        string `json:"Tag"`
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []imgEntry
		if json.Unmarshal([]byte(trimmed), &arr) != nil || len(arr) == 0 {
			return ""
		}
		if arr[0].Tag != "" && arr[0].Tag != "<none>" {
			return arr[0].Repository + ":" + arr[0].Tag
		}
		return arr[0].Repository
	}
	line := strings.SplitN(trimmed, "\n", 2)[0]
	var e imgEntry
	if json.Unmarshal([]byte(line), &e) != nil {
		return ""
	}
	if e.Tag != "" && e.Tag != "<none>" {
		return e.Repository + ":" + e.Tag
	}
	return e.Repository
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
