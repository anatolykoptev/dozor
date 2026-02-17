package engine

import (
	"context"
	"strings"
)

// RestartService restarts a docker compose service.
func (a *ServerAgent) RestartService(ctx context.Context, service string) CommandResult {
	return a.transport.DockerComposeCommand(ctx, "restart "+service)
}

// PruneDocker cleans up docker resources.
func (a *ServerAgent) PruneDocker(ctx context.Context, images, buildCache, volumes bool, age string) string {
	var results []string

	if images {
		cmd := "image prune -af"
		if age != "" {
			cmd += " --filter until=" + age
		}
		res := a.transport.DockerCommand(ctx, cmd)
		results = append(results, "Images: "+res.Output())
	}

	if buildCache {
		cmd := "builder prune -af"
		if age != "" {
			cmd += " --filter until=" + age
		}
		res := a.transport.DockerCommand(ctx, cmd)
		results = append(results, "Build cache: "+res.Output())
	}

	if volumes {
		res := a.transport.DockerCommand(ctx, "volume prune -f")
		results = append(results, "Volumes: "+res.Output())
	}

	// Show disk usage after
	diskRes := a.transport.DockerCommand(ctx, "system df")
	results = append(results, "\nDisk usage:\n"+diskRes.Output())

	return strings.Join(results, "\n")
}

// CleanupSystem scans or cleans system targets.
func (a *ServerAgent) CleanupSystem(ctx context.Context, targets []string, report bool, minAge string) string {
	if report {
		results := a.cleanup.Scan(ctx, targets)
		return FormatScanResults(results)
	}
	results := a.cleanup.Clean(ctx, targets, minAge)
	return FormatCleanResults(results)
}
