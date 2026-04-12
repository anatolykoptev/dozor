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

// containerInfo holds the parsed output of `docker compose ps --format json`.
type containerInfo struct {
	State      string          `json:"State"`
	Status     string          `json:"Status"`
	Publishers []portPublisher `json:"Publishers"`
}

// portPublisher represents a single port binding from `docker compose ps --format json`.
type portPublisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

// outputRunner is the function used to run external commands that need stdout captured.
// It can be replaced in tests.
var outputRunner = defaultOutputRunner

func defaultOutputRunner(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // trusted local config, not shell
	cmd.Dir = dir
	return cmd.Output()
}

// checkHealth verifies the container is running and its port bindings are present.
func checkHealth(ctx context.Context, composePath, service string) error {
	output, err := outputRunner(ctx, composePath,
		"docker", "compose", "ps", "--format", "json", service)
	if err != nil {
		return fmt.Errorf("check status: %w", err)
	}

	// `compose ps --format json` returns a JSON array or NDJSON depending on Docker version.
	trimmed := strings.TrimSpace(string(output))
	var c containerInfo
	if strings.HasPrefix(trimmed, "[") {
		var arr []containerInfo
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil || len(arr) == 0 {
			return fmt.Errorf("parse ps output: %w", err)
		}
		c = arr[0]
	} else {
		line := strings.SplitN(trimmed, "\n", 2)[0]
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return fmt.Errorf("parse ps output: %w", err)
		}
	}

	if !strings.EqualFold(c.State, "running") {
		return fmt.Errorf("not running: state=%s status=%s", c.State, c.Status)
	}

	if err := verifyPortMapping(ctx, composePath, service, c.Publishers); err != nil {
		return fmt.Errorf("port mapping: %w", err)
	}
	return nil
}

// verifyPortMapping checks that at least one published port is bound when the
// compose config declares ports for the service. Errors from fetching compose
// config are non-fatal (returns nil) so a misconfigured environment doesn't
// block deploys unnecessarily.
func verifyPortMapping(ctx context.Context, composePath, service string, publishers []portPublisher) error {
	cfgOut, cfgErr := outputRunner(ctx, composePath,
		"docker", "compose", "config", "--format", "json")
	if cfgErr != nil {
		return nil // can't verify — don't block deploy
	}

	var cfg struct {
		Services map[string]struct {
			Ports []any `json:"ports"`
		} `json:"services"`
	}
	if json.Unmarshal(cfgOut, &cfg) != nil {
		return nil
	}
	svcCfg, ok := cfg.Services[service]
	if !ok || len(svcCfg.Ports) == 0 {
		return nil // no ports declared — nothing to verify
	}

	for _, p := range publishers {
		if p.PublishedPort > 0 {
			return nil
		}
	}
	return fmt.Errorf("service %s declares ports but none are bound (race recovery: force-recreate)", service)
}

const (
	smokeTimeout      = 10 * time.Second
	smokeOKFloor      = 200
	smokeOKCeil       = 400
	smokeMaxRetries   = 5
	smokeRetryBackoff = 2 * time.Second
)

// smokeTest probes the configured SmokeURL with retries. Containers need a few
// seconds to bind their port after `docker compose up`, so a single-shot probe
// often hits connection reset. We retry up to smokeMaxRetries times with a fixed
// backoff. Any 2xx/3xx response on any attempt = success.
func smokeTest(ctx context.Context, url string) error {
	if url == "" {
		return nil
	}
	var lastErr error
	for attempt := range smokeMaxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("smoke test cancelled: %w", lastErr)
			case <-time.After(smokeRetryBackoff):
			}
		}
		lastErr = smokeProbe(ctx, url)
		if lastErr == nil {
			if attempt > 0 {
				slog.Info("deploy: smoke test passed after retry",
					"url", url, "attempt", attempt+1)
			}
			return nil
		}
		slog.Debug("deploy: smoke test retry",
			"url", url, "attempt", attempt+1, "error", lastErr)
	}
	return lastErr
}

func smokeProbe(ctx context.Context, url string) error {
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
