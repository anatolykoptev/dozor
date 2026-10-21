package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
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

	// MaxDeployTempLogs bounds each family of kept deploy diagnostic files
	// under os.TempDir() (dozor-build-*.log, dozor-up-*.log,
	// deploy-manual-*.log). The paths are surfaced in error messages and
	// status replies, so the newest files must stay readable — but without a
	// bound they accumulated in /tmp indefinitely (issue: temp leak). PruneTempLogs
	// keeps the newest MaxDeployTempLogs of a family and removes the rest.
	MaxDeployTempLogs = 20
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
		return fmt.Errorf("%w: %s", err, tail(string(output), maxOutputLen))
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

// composeFilePath returns the absolute path of the compose file every
// `docker compose` invocation is pinned to.
func composeFilePath(composePath string) string {
	return filepath.Join(composePath, "docker-compose.yml")
}

// composeArgv returns `docker compose` argv explicitly pinned to the deploy
// clone's compose file: {"compose", "-f", <composePath>/docker-compose.yml,
// args...}. Passing -f is the guard against Docker Compose's default
// override-file discovery: an untracked docker-compose.override.yml /
// compose.override.yml dropped into the clone must never silently extend a
// verified deploy (issue #239).
func composeArgv(composePath string, args ...string) []string {
	argv := []string{"compose", "-f", composeFilePath(composePath)}
	return append(argv, args...)
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

// PruneTempLogs removes all but the newest MaxDeployTempLogs files matching
// pattern inside os.TempDir(). Called right after a kept diagnostic file is
// written — the just-written file always survives. Best-effort: a removal
// failure is logged, never propagated, because the diagnostic it guards is
// already on disk and readable.
func PruneTempLogs(pattern string) {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), pattern))
	if err != nil || len(matches) <= MaxDeployTempLogs {
		return
	}
	type aged struct {
		path string
		mod  time.Time
	}
	files := make([]aged, 0, len(matches))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue // unstatable entries are kept — a bound, not a guarantee
		}
		files = append(files, aged{m, fi.ModTime()})
	}
	if len(files) <= MaxDeployTempLogs {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[MaxDeployTempLogs:] {
		if err := os.Remove(f.path); err != nil {
			slog.Warn("deploy: temp log prune failed", "path", f.path, "error", err)
		}
	}
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

// tail returns the LAST max bytes of s, marking truncation with a leading
// "...". Use for captured subprocess output (stderr_tail fields): the
// actionable error sits at the END of a docker build/up stream, after the
// progress spam — truncate() keeps the head, which showed "[internal] load
// build definition" while discarding the actual complaint (issue #165).
func tail(s string, max int) string {
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
