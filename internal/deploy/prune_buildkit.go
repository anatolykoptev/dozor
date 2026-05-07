package deploy

import (
	"context"
	"log/slog"
	"os/exec"
)

// pruneRunner invokes `docker <args...>` and returns the combined output.
// It is a package-level var so tests can substitute it.
var pruneRunner = defaultPruneRunner

func defaultPruneRunner(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // trusted local config, not shell
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// pruneBuildkitCacheMount runs `docker buildx prune --force --filter
// type=exec.cachemount` when req.Config.PruneBuildkitCache is true.
//
// This invalidates BuildKit exec cache mounts (the mechanism that keeps
// cargo target/ alive across --no-cache builds) so the subsequent
// docker compose build starts a true cold rebuild.
//
// Prune is best-effort: errors are logged at Warn but never block the build.
func pruneBuildkitCacheMount(ctx context.Context, req BuildRequest) {
	if !req.Config.PruneBuildkitCache {
		return
	}

	args := []string{"buildx", "prune", "--force", "--filter", "type=exec.cachemount"}
	out, err := pruneRunner(ctx, req.Config.ComposePath, args...)
	if err != nil {
		slog.Warn("deploy: buildkit cache mount prune failed (build will proceed)",
			"repo", req.Repo,
			"services", req.Config.Services,
			"output", string(out),
			"err", err,
		)
		return
	}
	slog.Info("deploy: buildkit cache mount pruned",
		"repo", req.Repo,
		"services", req.Config.Services,
		"output", string(out),
	)
}
