package deploy

import (
	"context"
	"fmt"
	"os/exec"
)

const (
	// maxOutputLen caps the stderr tail included in structured log lines for
	// the build phase. Build already writes a full dump file on failure, so
	// 500 chars in the log line is enough to orient the operator.
	maxOutputLen = 500

	// maxUpOutputLen caps the stderr tail included in structured log lines for
	// the up phase. Up-phase stderr (env-var warnings + the actual container
	// conflict) is denser, so we surface 4000 chars. A full dump file is also
	// written on failure (see runUpWithFullLog).
	maxUpOutputLen = 4000

	shortSHALen = 7
)

// cmdRunner is the function used to run external commands.
// It can be replaced in tests.
var cmdRunner = defaultRunCmd

func runCmd(ctx context.Context, dir, name string, args ...string) error {
	return cmdRunner(ctx, dir, name, args...)
}

//nolint:unused // DI default seam — assigned to var cmdRunner, swapped in tests
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
//
//nolint:unused // DI default seam — assigned to var buildRunner, swapped in tests
func defaultBuildRunner(ctx context.Context, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // trusted local config, not shell
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// defaultUpRunner runs `docker <args...>` and returns the full combined
// stdout+stderr regardless of outcome. Callers (runUpWithFullLog) dump the
// full output to a temp file on failure so operators can see the real error
// (e.g. "Container name already in use") buried beneath env-var warnings.
//
//nolint:unused // DI default seam — assigned to var upRunner, swapped in tests
func defaultUpRunner(ctx context.Context, dir string, args []string) ([]byte, error) {
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
	return ShortSHA(sha)
}

// ShortSHA truncates a commit hash to the package's standard short length
// (shortSHALen). Exported so external packages (e.g. internal/tools) emit
// SHAs that line up exactly with queue log lines.
func ShortSHA(sha string) string {
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
