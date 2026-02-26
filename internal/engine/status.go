package engine

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// StatusCollector gathers container status info.
type StatusCollector struct {
	transport *Transport
	discovery *DockerDiscovery // nil when SDK is unavailable (remote mode)
}

// dockerPSEntry is the JSON output from docker ps --format json.
type dockerPSEntry struct {
	Names   string `json:"Names"`
	State   string `json:"State"`
	Status  string `json:"Status"`
	RunTime string `json:"RunningFor"`
}

// dockerInspectEntry from docker inspect.
type dockerInspectEntry struct {
	State struct {
		Status        string `json:"Status"`
		Running       bool   `json:"Running"`
		StartedAt     string `json:"StartedAt"`
		RestartCount  int    `json:"RestartCount"`
		Health        *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
}

// GetContainerStatus returns status for a single service.
func (c *StatusCollector) GetContainerStatus(ctx context.Context, service string) ServiceStatus {
	// Prefer SDK-based inspection when available
	if c.discovery != nil {
		if status, ok := c.discovery.InspectContainer(ctx, service); ok {
			return status
		}
	}

	// Fallback to CLI (remote/SSH mode or SDK failure)
	return c.getContainerStatusCLI(ctx, service)
}

// containerNameMatches checks if any of the comma-separated container names matches the service.
func containerNameMatches(names, service string) bool {
	for _, n := range strings.Split(names, ",") {
		n = strings.TrimSpace(n)
		if n == service || strings.Contains(n, service) {
			return true
		}
	}
	return false
}

// getContainerStatusCLI uses docker CLI for container status (fallback).
func (c *StatusCollector) getContainerStatusCLI(ctx context.Context, service string) ServiceStatus {
	status := ServiceStatus{Name: service, State: StateUnknown}

	// Get from docker ps
	res := c.transport.DockerCommand(ctx, "ps --format json --filter name="+service)
	if !res.Success {
		return status
	}

	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		var entry dockerPSEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if !containerNameMatches(entry.Names, service) {
			continue
		}
		status.State = parseContainerState(entry.State)
		status.Uptime = entry.RunTime
	}

	// Get detailed info from docker inspect
	res = c.transport.DockerCommand(ctx, "inspect "+service+" 2>/dev/null")
	if res.Success && res.Stdout != "" {
		var entries []dockerInspectEntry
		if err := json.Unmarshal([]byte(res.Stdout), &entries); err == nil && len(entries) > 0 {
			entry := entries[0]
			status.RestartCount = entry.RestartCount
			if entry.State.Health != nil {
				status.Health = entry.State.Health.Status
			}
			if t, err := time.Parse(time.RFC3339Nano, entry.State.StartedAt); err == nil {
				status.StartedAt = t
			}
		}
	}

	return status
}

// DiscoverServices auto-discovers docker services.
// Uses SDK when available (local mode), falls back to CLI (remote/SSH mode).
func (c *StatusCollector) DiscoverServices(ctx context.Context) []string {
	// Prefer SDK-based discovery (cached, event-driven invalidation)
	if c.discovery != nil {
		return c.discovery.DiscoverServices(ctx)
	}

	// Fallback: CLI-based discovery via docker compose ps
	return c.discoverServicesCLI(ctx)
}

// discoverServicesCLI uses docker compose CLI for service discovery (fallback).
func (c *StatusCollector) discoverServicesCLI(ctx context.Context) []string {
	res := c.transport.DockerComposeCommand(ctx, "ps --format json -a")
	if !res.Success || res.Stdout == "" {
		return nil
	}
	var names []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Service string `json:"Service"`
			Name    string `json:"Name"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		svc := entry.Service
		if svc == "" {
			svc = entry.Name
		}
		if svc != "" && !seen[svc] {
			seen[svc] = true
			names = append(names, svc)
		}
	}
	return names
}

// GetAllStatuses returns status for all configured services.
func (c *StatusCollector) GetAllStatuses(ctx context.Context, services []string) []ServiceStatus {
	statuses := make([]ServiceStatus, 0, len(services))
	for _, svc := range services {
		statuses = append(statuses, c.GetContainerStatus(ctx, svc))
	}
	return statuses
}

func parseContainerState(s string) ContainerState {
	switch strings.ToLower(s) {
	case "running":
		return StateRunning
	case "exited":
		return StateExited
	case "restarting":
		return StateRestarting
	case "paused":
		return StatePaused
	case "dead":
		return StateDead
	default:
		return StateUnknown
	}
}
