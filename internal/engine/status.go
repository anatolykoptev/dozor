package engine

import (
	"context"
	"encoding/json"
	"strings"
)

// StatusCollector gathers container status info.
type StatusCollector struct {
	transport *Transport
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
		RestartCount  int    `json:"RestartCount"`
		Health        *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
}

// GetContainerStatus returns status for a single service.
func (c *StatusCollector) GetContainerStatus(ctx context.Context, service string) ServiceStatus {
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
		// Match exact service name (docker ps may return partial matches)
		names := strings.Split(entry.Names, ",")
		matched := false
		for _, n := range names {
			n = strings.TrimSpace(n)
			// Container names often have project prefix: "project-service-1"
			if n == service || strings.Contains(n, service) {
				matched = true
				break
			}
		}
		if !matched {
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
		}
	}

	return status
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
