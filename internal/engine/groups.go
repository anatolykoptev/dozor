package engine

import (
	"log"
	"sort"
	"strings"
)

const (
	// healthHealthy is the string for a healthy group.
	healthHealthy = "healthy"
	// healthDegraded is the string for a degraded group.
	healthDegraded = "degraded"
	// healthSeverityCritical is the numeric severity for critical health.
	healthSeverityCritical = 3
	// healthSeverityDegraded is the numeric severity for degraded health.
	healthSeverityDegraded = 2
	// healthSeverityWarning is the numeric severity for warning health.
	healthSeverityWarning = 1
)

// BuildDependencyGraph constructs a graph from dozor.depends_on labels.
// Each service's label is parsed as comma-separated dependency names.
// Dangling references (deps not in statuses) are logged and skipped.
func BuildDependencyGraph(statuses []ServiceStatus) DependencyGraph {
	known := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		known[s.Name] = true
	}

	graph := make(DependencyGraph)
	for _, s := range statuses {
		raw := s.DozorLabel("depends_on")
		if raw == "" {
			continue
		}
		var deps []string
		for _, d := range strings.Split(raw, ",") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if !known[d] {
				log.Printf("[warn] service %s depends_on %q which is not discovered", s.Name, d)
				continue
			}
			deps = append(deps, d)
		}
		if len(deps) > 0 {
			graph[s.Name] = deps
		}
	}
	return graph
}

// GroupServices organizes statuses by dozor.group label.
// Groups are sorted alphabetically; ungrouped services (empty label) are sorted last.
// Each group's Health is the worst member health: critical > degraded > warning > healthy.
func GroupServices(statuses []ServiceStatus) []ServiceGroup {
	buckets := make(map[string][]ServiceStatus)
	for _, s := range statuses {
		group := s.DozorLabel("group")
		buckets[group] = append(buckets[group], s)
	}

	var groups []ServiceGroup
	for name, members := range buckets {
		groups = append(groups, ServiceGroup{
			Name:     name,
			Services: members,
			Health:   worstHealth(members),
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		// Ungrouped (empty name) sorts last
		if groups[i].Name == "" {
			return false
		}
		if groups[j].Name == "" {
			return true
		}
		return groups[i].Name < groups[j].Name
	})

	return groups
}

// worstHealth returns the worst health level across services.
func worstHealth(statuses []ServiceStatus) string {
	worst := healthHealthy
	for _, s := range statuses {
		level := serviceHealthLevel(s)
		if healthSeverity(level) > healthSeverity(worst) {
			worst = level
		}
	}
	return worst
}

// serviceHealthLevel maps a service status to a health string.
func serviceHealthLevel(s ServiceStatus) string {
	if s.State != StateRunning {
		return string(AlertCritical)
	}
	if s.HealthcheckOK != nil && !*s.HealthcheckOK {
		return healthDegraded
	}
	if s.RestartCount > 0 || s.ErrorCount > maxRecentErrors {
		return healthDegraded
	}
	if s.ErrorCount > 0 {
		return string(AlertWarning)
	}
	return healthHealthy
}

// healthSeverity returns a numeric severity for ordering.
func healthSeverity(health string) int {
	switch health {
	case string(AlertCritical):
		return healthSeverityCritical
	case healthDegraded:
		return healthSeverityDegraded
	case string(AlertWarning):
		return healthSeverityWarning
	default:
		return 0
	}
}
