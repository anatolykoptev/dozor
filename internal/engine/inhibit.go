package engine

import "log/slog"

// Inhibit filters alerts based on dependency relationships.
// If a parent service is down, alerts from dependent services are suppressed.
// Returns the kept alerts and the inhibited alerts separately.
func Inhibit(alerts []Alert, statuses []ServiceStatus) (kept, inhibited []Alert) {
	graph := BuildDependencyGraph(statuses)

	// Build set of down services (not running).
	down := make(map[string]bool)
	for _, s := range statuses {
		if s.State != StateRunning {
			down[s.Name] = true
		}
	}

	if len(down) == 0 {
		return alerts, nil
	}

	// Build set of services that should be inhibited:
	// any service that transitively depends on a down service.
	inhibitSet := make(map[string]bool)
	for svc := range down {
		for _, dep := range graph.Dependents(svc) {
			// Don't inhibit the parent itself — we want that alert.
			if !down[dep] {
				inhibitSet[dep] = true
			}
		}
	}

	if len(inhibitSet) == 0 {
		return alerts, nil
	}

	for _, a := range alerts {
		if inhibitSet[a.Service] {
			// Find which parent is causing the inhibition.
			parentDown := findDownParent(a.Service, graph, down)
			slog.Info("alert inhibited by dependency",
				slog.String("service", a.Service),
				slog.String("parent_down", parentDown),
				slog.String("alert", a.Title))
			inhibited = append(inhibited, a)
		} else {
			kept = append(kept, a)
		}
	}
	return
}

// findDownParent returns the name of the first direct dependency that is down.
func findDownParent(service string, graph DependencyGraph, down map[string]bool) string {
	for _, dep := range graph[service] {
		if down[dep] {
			return dep
		}
	}
	return string(StateUnknown)
}
