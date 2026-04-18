package deploy

import (
	"context"
	"fmt"
	"os/exec"
)

const (
	maxOutputLen = 500
	shortSHALen  = 7
)

// cmdRunner is the function used to run external commands.
// It can be replaced in tests.
var cmdRunner = defaultRunCmd

func runCmd(ctx context.Context, dir, name string, args ...string) error {
	return cmdRunner(ctx, dir, name, args...)
}

func defaultRunCmd(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // trusted local config, not shell
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, truncate(string(output), maxOutputLen))
	}
	return nil
}

// defaultBuildRunner runs `docker <args...>` and returns the full combined
// stdout+stderr regardless of outcome. Callers (runBuildWithFullLog) dump
// the full output to a temp file on failure so operators can inspect what
// Docker actually complained about.
func defaultBuildRunner(ctx context.Context, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // trusted local config, not shell
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func serviceKey(services []string) string {
	s := ""
	for i, svc := range services {
		if i > 0 {
			s += "+"
		}
		s += svc
	}
	return s
}

func short(sha string) string {
	if len(sha) > shortSHALen {
		return sha[:shortSHALen]
	}
	return sha
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
