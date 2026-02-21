package engine

import (
	"context"
	"fmt"
	"strings"
)

// RestartService restarts a docker compose service and its transitive dependents.
// If the primary restart fails, returns immediately. If no dozor.depends_on labels
// exist, behaves identically to before (single service restart).
func (a *ServerAgent) RestartService(ctx context.Context, service string) CommandResult {
	result := a.transport.DockerComposeCommand(ctx, "restart "+service)
	if !result.Success {
		return result
	}

	// Build dependency graph from all discovered services
	allServices := a.resolveServices(ctx, nil)
	statuses := a.status.GetAllStatuses(ctx, allServices)
	graph := BuildDependencyGraph(statuses)
	dependents := graph.Dependents(service)

	if len(dependents) == 0 {
		return result
	}

	// Restart dependents in order (dependencies-first from BFS)
	var restarted []string
	for _, dep := range dependents {
		r := a.transport.DockerComposeCommand(ctx, "restart "+dep)
		if r.Success {
			restarted = append(restarted, dep)
		}
	}

	if len(restarted) > 0 {
		result.Stdout = fmt.Sprintf("Restarted %s (+ dependents: %s)", service, strings.Join(restarted, ", "))
	}
	return result
}

// PruneDocker cleans up docker resources.
func (a *ServerAgent) PruneDocker(ctx context.Context, images, buildCache, volumes bool, age string) string {
	var results []string

	// Always prune stopped containers first
	cmd := "container prune -f"
	if age != "" {
		cmd += " --filter until=" + age
	}
	res := a.transport.DockerCommand(ctx, cmd)
	results = append(results, "Containers: "+res.Output())

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

	// Prune unused networks
	netRes := a.transport.DockerCommand(ctx, "network prune -f")
	results = append(results, "Networks: "+netRes.Output())

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
