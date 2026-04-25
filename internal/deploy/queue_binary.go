package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

const (
	binaryHealthWait        = 3 * time.Second
	canaryDefaultTimeout    = 30 * time.Second
	canaryDefaultWindow     = 30 * time.Second
	canaryPollInterval      = 5 * time.Second
	serviceActiveMaxRetries = 6
	serviceActiveRetryDelay = 500 * time.Millisecond
)

// systemctlRunner is the function used to invoke systemctl.
// It can be replaced in tests.
var systemctlRunner = defaultSystemctlRunner

func defaultSystemctlRunner(ctx context.Context, args ...string) ([]byte, error) {
	//nolint:gosec // args come from trusted deploy-repos.yaml, not user input
	return exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
}

// executeBinaryBuild handles the "binary" deploy kind:
//  1. git pull --ff-only in SourcePath
//  2. run BuildCmd in SourcePath (e.g. go build -o /usr/local/bin/svc ./cmd/svc)
//  3. canary restart: restart user_services[0], wait for smoke_url to stay 200 for canary_smoke_window
//  4. batch restart: restart user_services[1:] in parallel
func executeBinaryBuild(ctx context.Context, req BuildRequest) BuildResult {
	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.UserServices,
	}

	// Step 1: pull latest source.
	slog.Info("deploy/binary: git pull", "repo", req.Repo, "path", req.Config.SourcePath)
	if err := runCmd(ctx, req.Config.SourcePath, "git", "pull", "--ff-only"); err != nil {
		result.Error = fmt.Sprintf("git pull: %v", err)
		return result
	}

	// Step 2: build binary.
	cmd := req.Config.BuildCmd
	slog.Info("deploy/binary: build", "repo", req.Repo, "cmd", cmd)
	if err := runCmd(ctx, req.Config.SourcePath, cmd[0], cmd[1:]...); err != nil {
		result.Error = fmt.Sprintf("build: %v", err)
		return result
	}

	// Step 3: restart services with canary logic.
	if err := restartWithCanary(ctx, req.Config); err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	return result
}

// restartWithCanary restarts user_services in two stages:
//   - Stage 1 (canary): restart first service, wait for smoke_url to stay 200
//     for canary_smoke_window (sustained window).
//   - Stage 2 (batch): restart remaining services in parallel.
//
// If there is only one service, it is restarted and smoke-tested normally.
// On canary failure the remaining services are NOT restarted (they keep the
// prior in-memory binary via Linux inode refcount).
func restartWithCanary(ctx context.Context, cfg RepoConfig) error {
	svcs := cfg.UserServices
	if len(svcs) == 0 {
		return nil
	}

	smokeTimeout := cfg.CanarySmokeTimeout.OrDefault(canaryDefaultTimeout)
	smokeWindow := cfg.CanarySmokeWindow.OrDefault(canaryDefaultWindow)

	canary := svcs[0]
	remaining := svcs[1:]

	slog.Info("deploy/binary: canary restart", "service", canary,
		"smoke_url", cfg.SmokeURL,
		"smoke_timeout", smokeTimeout,
		"smoke_window", smokeWindow)

	if err := restartAndSmoke(ctx, canary, cfg.SmokeURL, smokeTimeout, smokeWindow); err != nil {
		return fmt.Errorf("canary %s failed: %w", canary, err)
	}

	if len(remaining) == 0 {
		slog.Info("deploy/binary: single service canary passed, done", "service", canary)
		return nil
	}

	slog.Info("deploy/binary: canary OK, restarting remaining services",
		"count", len(remaining), "services", remaining)

	return restartParallel(ctx, remaining)
}

// restartAndSmoke restarts a single systemd user service, waits for it to
// become active, then sustains a 200 response on smokeURL for the full
// smokeWindow (polled every canaryPollInterval). If smokeURL is empty the
// smoke-window check is skipped.
func restartAndSmoke(ctx context.Context, svc, smokeURL string, smokeTimeout, smokeWindow time.Duration) error {
	if err := restartService(ctx, svc); err != nil {
		return err
	}

	if err := waitServiceActive(ctx, svc); err != nil {
		return err
	}

	if smokeURL == "" {
		return nil
	}

	// Wait for first 200 within smokeTimeout.
	slog.Info("deploy/binary: waiting for initial smoke 200",
		"service", svc, "url", smokeURL, "timeout", smokeTimeout)
	firstCtx, cancel := context.WithTimeout(ctx, smokeTimeout)
	defer cancel()
	if err := waitFor200(firstCtx, smokeURL); err != nil {
		return fmt.Errorf("smoke URL %s did not return 200 within %s: %w", smokeURL, smokeTimeout, err)
	}

	// Sustain 200 for smokeWindow.
	slog.Info("deploy/binary: sustaining smoke window",
		"service", svc, "url", smokeURL, "window", smokeWindow)
	if err := sustainSmoke(ctx, smokeURL, smokeWindow); err != nil {
		return fmt.Errorf("smoke URL %s failed during window: %w", smokeURL, err)
	}

	slog.Info("deploy/binary: canary smoke passed", "service", svc, "url", smokeURL)
	return nil
}

// restartService runs systemctl --user restart <svc>.
func restartService(ctx context.Context, svc string) error {
	slog.Info("deploy/binary: restart service", "service", svc)
	out, err := systemctlRunner(ctx, "--user", "restart", svc)
	if err != nil {
		return fmt.Errorf("restart %s: %w: %s", svc, err, out)
	}
	return nil
}

// waitServiceActive polls `systemctl --user is-active <svc>` until it returns
// "active" or the retry budget is exhausted.
func waitServiceActive(ctx context.Context, svc string) error {
	for i := range serviceActiveMaxRetries {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(serviceActiveRetryDelay):
			}
		}
		out, _ := systemctlRunner(ctx, "--user", "is-active", svc)
		if string(out) == "active\n" {
			return nil
		}
	}
	out, _ := systemctlRunner(ctx, "--user", "is-active", svc)
	return fmt.Errorf("service %s not active after restart (state: %s)", svc, string(out))
}

// waitFor200 polls smokeURL every canaryPollInterval until a 200 is received or ctx expires.
func waitFor200(ctx context.Context, smokeURL string) error {
	var lastErr error
	for {
		lastErr = smokeProbe(ctx, smokeURL)
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-time.After(canaryPollInterval):
		}
	}
}

// sustainSmoke polls smokeURL every canaryPollInterval for the full duration.
// A single non-200 response fails the window.
func sustainSmoke(ctx context.Context, smokeURL string, window time.Duration) error {
	deadline := time.Now().Add(window)
	polls := 0
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(canaryPollInterval):
		}
		if err := smokeProbe(ctx, smokeURL); err != nil {
			return fmt.Errorf("smoke failed after %d successful polls: %w", polls, err)
		}
		polls++
		slog.Debug("deploy/binary: smoke window poll OK",
			"url", smokeURL, "polls", polls,
			"remaining", time.Until(deadline).Round(time.Second))
	}
	return nil
}

// restartParallel restarts multiple services concurrently and waits for all.
// Returns the first error encountered (remaining errors are logged).
func restartParallel(ctx context.Context, svcs []string) error {
	var (
		mu      sync.Mutex
		firstErr error
		wg      sync.WaitGroup
	)
	for _, svc := range svcs {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			if err := restartService(ctx, s); err != nil {
				slog.Error("deploy/binary: parallel restart failed", "service", s, "error", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			} else {
				slog.Info("deploy/binary: parallel restart OK", "service", s)
			}
		}(svc)
	}
	wg.Wait()
	// Brief settle before returning so services can bind ports.
	select {
	case <-ctx.Done():
	case <-time.After(binaryHealthWait):
	}
	return firstErr
}
