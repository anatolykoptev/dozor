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
// Returns empty string if the image does not exist or the command fails.
func composeImageID(ctx context.Context, composePath, svc string) string {
	cmd := exec.CommandContext(ctx, "docker", "compose", "config", "--format", "json")
	cmd.Dir = composePath
	out, err := cmd.Output()
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
	image := cfg.Services[svc].Image
	if image == "" {
		return ""
	}
	// image is sourced from `docker compose config` (local trusted YAML) and passed as an
	// individual argv slot — not interpolated into a shell, so command injection is not
	// reachable here. gosec flags any variable passed to exec regardless of context.
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", image, "--format", "{{.Id}}") //nolint:gosec // trusted compose-config input, not shell
	idOut, err := inspect.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(idOut))
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
