package engine

import (
	"strings"
	"testing"
)

func TestGroupServicesEmpty(t *testing.T) {
	groups := GroupServices(nil)
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}

func TestGroupServicesNoLabels(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "nginx", State: StateRunning},
		{Name: "redis", State: StateRunning},
	}
	groups := GroupServices(statuses)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group (ungrouped), got %d", len(groups))
	}
	if groups[0].Name != "" {
		t.Errorf("expected empty group name, got %q", groups[0].Name)
	}
	if len(groups[0].Services) != 2 {
		t.Errorf("expected 2 services in ungrouped, got %d", len(groups[0].Services))
	}
	if groups[0].Health != "healthy" {
		t.Errorf("expected healthy, got %q", groups[0].Health)
	}
}

func TestGroupServicesWithLabels(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "postgres", State: StateRunning, Labels: map[string]string{"dozor.group": "data"}},
		{Name: "redis", State: StateRunning, Labels: map[string]string{"dozor.group": "data"}},
		{Name: "my-api", State: StateRunning, Labels: map[string]string{"dozor.group": "backend"}},
		{Name: "nginx", State: StateRunning},
	}
	groups := GroupServices(statuses)

	// Should be: backend, data, ungrouped (alphabetical, ungrouped last)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if groups[0].Name != "backend" {
		t.Errorf("expected first group 'backend', got %q", groups[0].Name)
	}
	if groups[1].Name != "data" {
		t.Errorf("expected second group 'data', got %q", groups[1].Name)
	}
	if groups[2].Name != "" {
		t.Errorf("expected third group '' (ungrouped), got %q", groups[2].Name)
	}
}

func TestGroupServicesHealth(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "postgres", State: StateExited, Labels: map[string]string{"dozor.group": "data"}},
		{Name: "redis", State: StateRunning, Labels: map[string]string{"dozor.group": "data"}},
	}
	groups := GroupServices(statuses)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Health != "critical" {
		t.Errorf("expected critical (postgres exited), got %q", groups[0].Health)
	}
}

func TestBuildDependencyGraphEmpty(t *testing.T) {
	graph := BuildDependencyGraph(nil)
	if len(graph) != 0 {
		t.Errorf("expected empty graph, got %d entries", len(graph))
	}
}

func TestBuildDependencyGraphNoLabels(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "postgres", State: StateRunning},
		{Name: "redis", State: StateRunning},
	}
	graph := BuildDependencyGraph(statuses)
	if len(graph) != 0 {
		t.Errorf("expected empty graph, got %d entries", len(graph))
	}
}

func TestBuildDependencyGraph(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "postgres", State: StateRunning},
		{Name: "redis", State: StateRunning},
		{Name: "my-api", State: StateRunning, Labels: map[string]string{"dozor.depends_on": "postgres,redis"}},
		{Name: "my-worker", State: StateRunning, Labels: map[string]string{"dozor.depends_on": "postgres,my-api"}},
	}
	graph := BuildDependencyGraph(statuses)

	if len(graph) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(graph))
	}
	if len(graph["my-api"]) != 2 {
		t.Errorf("my-api should have 2 deps, got %d", len(graph["my-api"]))
	}
	if len(graph["my-worker"]) != 2 {
		t.Errorf("my-worker should have 2 deps, got %d", len(graph["my-worker"]))
	}
}

func TestBuildDependencyGraphDanglingRef(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "my-api", State: StateRunning, Labels: map[string]string{"dozor.depends_on": "nonexistent,postgres"}},
		{Name: "postgres", State: StateRunning},
	}
	graph := BuildDependencyGraph(statuses)

	// Should only have postgres, nonexistent is dropped
	deps := graph["my-api"]
	if len(deps) != 1 || deps[0] != "postgres" {
		t.Errorf("expected [postgres], got %v", deps)
	}
}

func TestDependentsSimple(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "postgres", State: StateRunning},
		{Name: "redis", State: StateRunning},
		{Name: "my-api", State: StateRunning, Labels: map[string]string{"dozor.depends_on": "postgres,redis"}},
		{Name: "my-worker", State: StateRunning, Labels: map[string]string{"dozor.depends_on": "postgres,my-api"}},
	}
	graph := BuildDependencyGraph(statuses)

	deps := graph.Dependents("postgres")
	// postgres -> my-api, my-worker (both depend on postgres directly or transitively)
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependents of postgres, got %d: %v", len(deps), deps)
	}

	// my-api should come before my-worker (BFS order: my-api is direct, my-worker via my-api)
	if deps[0] != "my-api" {
		t.Errorf("expected my-api first, got %s", deps[0])
	}
	if deps[1] != "my-worker" {
		t.Errorf("expected my-worker second, got %s", deps[1])
	}
}

func TestDependentsNone(t *testing.T) {
	statuses := []ServiceStatus{
		{Name: "postgres", State: StateRunning},
		{Name: "my-api", State: StateRunning, Labels: map[string]string{"dozor.depends_on": "postgres"}},
	}
	graph := BuildDependencyGraph(statuses)

	// my-api has no dependents
	deps := graph.Dependents("my-api")
	if len(deps) != 0 {
		t.Errorf("expected 0 dependents of my-api, got %d", len(deps))
	}
}

func TestDependentsCycle(t *testing.T) {
	// Manually construct a cycle: A -> B -> A
	graph := DependencyGraph{
		"a": {"b"},
		"b": {"a"},
	}
	deps := graph.Dependents("a")
	// Should find b but not loop forever
	if len(deps) != 1 || deps[0] != "b" {
		t.Errorf("expected [b], got %v", deps)
	}
}

func TestDependentsUnknownService(t *testing.T) {
	graph := DependencyGraph{
		"my-api": {"postgres"},
	}
	deps := graph.Dependents("unknown")
	if len(deps) != 0 {
		t.Errorf("expected 0 dependents, got %d", len(deps))
	}
}

func TestFormatGroups(t *testing.T) {
	groups := []ServiceGroup{
		{
			Name: "data",
			Services: []ServiceStatus{
				{Name: "postgres", State: StateExited},
				{Name: "redis", State: StateRunning},
			},
			Health: "critical",
		},
		{
			Name: "backend",
			Services: []ServiceStatus{
				{Name: "my-api", State: StateRunning},
			},
			Health: "healthy",
		},
	}
	output := FormatGroups(groups)

	if !strings.Contains(output, "Service Groups") {
		t.Error("expected header")
	}
	if !strings.Contains(output, "[CRITICAL] data") {
		t.Error("expected [CRITICAL] data")
	}
	if !strings.Contains(output, "[HEALTHY] backend") {
		t.Error("expected [HEALTHY] backend")
	}
	if !strings.Contains(output, "[!!] postgres: exited") {
		t.Error("expected postgres exited")
	}
	if !strings.Contains(output, "[OK] redis: running") {
		t.Error("expected redis OK")
	}
}

func TestGenerateGroupAlerts(t *testing.T) {
	groups := []ServiceGroup{
		{Name: "data", Health: "critical", Services: []ServiceStatus{{Name: "postgres"}}},
		{Name: "backend", Health: "healthy", Services: []ServiceStatus{{Name: "my-api"}}},
		{Name: "", Health: "degraded", Services: []ServiceStatus{{Name: "nginx"}}},
	}
	alerts := GenerateGroupAlerts(groups)

	// Only data should generate alert (critical). Ungrouped (empty name) is skipped.
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Service != "group:data" {
		t.Errorf("expected service 'group:data', got %q", alerts[0].Service)
	}
	if alerts[0].Level != AlertCritical {
		t.Errorf("expected critical level, got %s", alerts[0].Level)
	}
}

func TestFormatGroupsUngrouped(t *testing.T) {
	groups := []ServiceGroup{
		{
			Name: "",
			Services: []ServiceStatus{
				{Name: "nginx", State: StateRunning},
			},
			Health: "healthy",
		},
	}
	output := FormatGroups(groups)
	if !strings.Contains(output, "ungrouped") {
		t.Error("expected 'ungrouped' label for empty group name")
	}
}
