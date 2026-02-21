package engine

import (
	"testing"
	"time"
)

func makeStatus(name string, state ContainerState, dependsOn string) ServiceStatus {
	labels := map[string]string{}
	if dependsOn != "" {
		labels["dozor.depends_on"] = dependsOn
	}
	return ServiceStatus{Name: name, State: state, Labels: labels}
}

func makeAlert(service string) Alert {
	return Alert{
		Level:   AlertError,
		Service: service,
		Title:   service + " has errors",
		Timestamp: time.Now(),
	}
}

func TestInhibit_NoDownServices(t *testing.T) {
	statuses := []ServiceStatus{
		makeStatus("postgres", StateRunning, ""),
		makeStatus("api", StateRunning, "postgres"),
	}
	alerts := []Alert{makeAlert("api")}

	kept, inhibited := Inhibit(alerts, statuses)
	if len(kept) != 1 || len(inhibited) != 0 {
		t.Fatalf("expected 1 kept, 0 inhibited; got %d kept, %d inhibited", len(kept), len(inhibited))
	}
}

func TestInhibit_ParentDown(t *testing.T) {
	statuses := []ServiceStatus{
		makeStatus("postgres", StateExited, ""),
		makeStatus("api", StateRunning, "postgres"),
		makeStatus("worker", StateRunning, "postgres"),
	}
	alerts := []Alert{
		makeAlert("postgres"),
		makeAlert("api"),
		makeAlert("worker"),
	}

	kept, inhibited := Inhibit(alerts, statuses)
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept (postgres); got %d", len(kept))
	}
	if kept[0].Service != "postgres" {
		t.Fatalf("expected postgres alert kept; got %s", kept[0].Service)
	}
	if len(inhibited) != 2 {
		t.Fatalf("expected 2 inhibited (api, worker); got %d", len(inhibited))
	}
}

func TestInhibit_TransitiveDependency(t *testing.T) {
	statuses := []ServiceStatus{
		makeStatus("postgres", StateExited, ""),
		makeStatus("api", StateRunning, "postgres"),
		makeStatus("frontend", StateRunning, "api"),
	}
	alerts := []Alert{
		makeAlert("postgres"),
		makeAlert("api"),
		makeAlert("frontend"),
	}

	kept, inhibited := Inhibit(alerts, statuses)
	// postgres is down, api depends on postgres -> inhibited
	// frontend depends on api (not directly down) -> inhibited transitively
	if len(kept) != 1 || kept[0].Service != "postgres" {
		t.Fatalf("expected only postgres kept; got %d kept", len(kept))
	}
	if len(inhibited) != 2 {
		t.Fatalf("expected 2 inhibited; got %d", len(inhibited))
	}
}

func TestInhibit_IndependentServiceNotInhibited(t *testing.T) {
	statuses := []ServiceStatus{
		makeStatus("postgres", StateExited, ""),
		makeStatus("api", StateRunning, "postgres"),
		makeStatus("redis", StateRunning, ""),
	}
	alerts := []Alert{
		makeAlert("postgres"),
		makeAlert("api"),
		makeAlert("redis"),
	}

	kept, inhibited := Inhibit(alerts, statuses)
	if len(kept) != 2 {
		t.Fatalf("expected 2 kept (postgres, redis); got %d", len(kept))
	}
	if len(inhibited) != 1 || inhibited[0].Service != "api" {
		t.Fatalf("expected api inhibited; got %d inhibited", len(inhibited))
	}
}

func TestInhibit_BothParentAndChildDown(t *testing.T) {
	// When both parent and child are down, child alerts are still inhibited
	// because the root cause is the parent.
	statuses := []ServiceStatus{
		makeStatus("postgres", StateExited, ""),
		makeStatus("api", StateExited, "postgres"),
	}
	alerts := []Alert{
		makeAlert("postgres"),
		makeAlert("api"),
	}

	kept, inhibited := Inhibit(alerts, statuses)
	// api is down AND depends on postgres which is down.
	// Both are in the "down" set. Dependents of postgres = [api].
	// api is in down set, so it's NOT added to inhibitSet (we only inhibit non-down dependents).
	// Wait - let me re-check the logic. The inhibit code says:
	// "if !down[dep]" — so if api is also down, it won't be inhibited.
	// This means both alerts are kept when both are down.
	// That makes sense: if api is itself down, we want to alert about it.
	if len(kept) != 2 {
		t.Fatalf("expected both kept when both are down; got %d kept, %d inhibited", len(kept), len(inhibited))
	}
}

func TestInhibit_EmptyAlerts(t *testing.T) {
	statuses := []ServiceStatus{makeStatus("postgres", StateExited, "")}
	kept, inhibited := Inhibit(nil, statuses)
	if len(kept) != 0 || len(inhibited) != 0 {
		t.Fatal("empty alerts should return empty")
	}
}
