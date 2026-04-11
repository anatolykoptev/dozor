package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

const (
	smokeTimeout = 10 * time.Second
	smokeOKFloor = 200
	smokeOKCeil  = 400
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
		var arr []struct {
			ID            string `json:"ID"`
			ContainerName string `json:"ContainerName"`
		}
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
	// NDJSON fallback.
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e struct {
			ID            string `json:"ID"`
			ContainerName string `json:"ContainerName"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.ContainerName == svc || strings.HasSuffix(e.ContainerName, "_"+svc) {
			return e.ID
		}
	}
	return ""
}

// logImageDiff warns for any service whose image ID did not change across `compose build`.
// A no-op build is legitimate when source did not change, but suspicious when a commit
// just landed — it usually means layer cache is stale or COPY paths are wrong.
func logImageDiff(before, after map[string]string, services []string, commit string) {
	var unchanged []string
	for _, svc := range services {
		if before[svc] != "" && before[svc] == after[svc] {
			unchanged = append(unchanged, svc)
		}
	}
	if len(unchanged) == 0 {
		return
	}
	slog.Warn("deploy: build produced no new image — cache hit or stale COPY",
		"services", unchanged,
		"commit", short(commit),
		"hint", "if source changed, inspect Dockerfile COPY paths or retry with no_cache: true",
	)
}

// smokeTest performs a single HTTP GET against the configured SmokeURL. Any 2xx/3xx
// response is considered healthy. Returns an error on non-2xx or transport failure.
func smokeTest(ctx context.Context, url string) error {
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, smokeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < smokeOKFloor || resp.StatusCode >= smokeOKCeil {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
