package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// GetSystemdStatus returns status of local systemd services.
// Uses config list first, then auto-discovers active user services as fallback.
func (a *ServerAgent) GetSystemdStatus(ctx context.Context, services []string) string {
	if len(services) == 0 {
		services = a.cfg.SystemdServices
	}
	if len(services) == 0 {
		// Auto-discover user services as fallback
		discovered := a.DiscoverUserServices(ctx)
		for _, svc := range discovered {
			services = append(services, svc.Name)
		}
	}
	if len(services) == 0 {
		return "No systemd services found (auto-discovery found none, or set DOZOR_SYSTEMD_SERVICES in .env)."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Systemd Services (%d)\n\n", len(services))

	for _, svc := range services {
		state := a.systemctlIsActive(ctx, svc)
		icon := "OK"
		if state != "active" {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", icon, svc, state)

		// Get memory and uptime from systemctl show
		output := a.systemctlShow(ctx, svc, "ActiveEnterTimestamp,MemoryCurrent")
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ActiveEnterTimestamp=") {
				ts := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
				if ts != "" {
					fmt.Fprintf(&b, "  Started: %s\n", ts)
				}
			}
			if strings.HasPrefix(line, "MemoryCurrent=") {
				mem := strings.TrimPrefix(line, "MemoryCurrent=")
				if mem != "" && mem != "[not set]" && mem != "18446744073709551615" {
					if mb, ok := BytesToMB(mem); ok {
						fmt.Fprintf(&b, "  Memory: %.1f MB\n", mb)
					}
				}
			}
		}
	}

	return b.String()
}

// systemctlIsActive checks service status, trying --user first then system.
func (a *ServerAgent) systemctlIsActive(ctx context.Context, svc string) string {
	res := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl --user is-active %s 2>/dev/null", svc))
	state := strings.TrimSpace(res.Stdout)
	if state == "active" || state == "activating" || state == "deactivating" {
		return state
	}
	res = a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl is-active %s 2>/dev/null", svc))
	state = strings.TrimSpace(res.Stdout)
	if state == "" {
		return "unknown"
	}
	return state
}

// systemctlShow gets properties from systemctl show, trying --user first.
func (a *ServerAgent) systemctlShow(ctx context.Context, svc, properties string) string {
	res := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl --user show %s --property=%s 2>/dev/null", svc, properties))
	if res.Success && res.Stdout != "" && !strings.Contains(res.Stdout, "No such file") {
		return res.Stdout
	}
	res = a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl show %s --property=%s 2>/dev/null", svc, properties))
	return res.Stdout
}

// ResolveUserServices returns configured user services, falling back to auto-discovery.
func (a *ServerAgent) ResolveUserServices(ctx context.Context) []UserService {
	if a.cfg.HasUserServices() {
		return a.cfg.UserServices
	}
	return a.DiscoverUserServices(ctx)
}

// FindUserServiceIn finds a service by name in a list.
func FindUserServiceIn(services []UserService, name string) *UserService {
	for i := range services {
		if services[i].Name == name {
			return &services[i]
		}
	}
	return nil
}

// UserServiceNamesFrom returns just the names from a service list.
func UserServiceNamesFrom(services []UserService) []string {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.Name
	}
	return names
}

// DiscoverUserServices scans active user systemd services.
// Returns discovered services with ports (from ListenStream in unit files).
func (a *ServerAgent) DiscoverUserServices(ctx context.Context) []UserService {
	res := a.transport.ExecuteUnsafe(ctx,
		"systemctl --user list-units --type=service --state=active --no-pager --plain --no-legend 2>/dev/null")
	if !res.Success || res.Stdout == "" {
		return nil
	}

	var services []UserService
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "unit.service loaded active running Description..."
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		if !strings.HasSuffix(unit, ".service") {
			continue
		}
		// Skip system-level services that appear in user context
		name := strings.TrimSuffix(unit, ".service")
		if isSystemService(name) {
			continue
		}

		svc := UserService{Name: name}

		// Try to detect port from unit file
		svc.Port = a.detectServicePort(ctx, name)

		services = append(services, svc)
	}
	return services
}

// detectServicePort tries to find the listening port for a user service.
func (a *ServerAgent) detectServicePort(ctx context.Context, name string) int {
	// Check ListenStream from socket unit
	res := a.transport.ExecuteUnsafe(ctx,
		fmt.Sprintf("systemctl --user show %s.socket --property=ListenStream 2>/dev/null", name))
	if res.Success {
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.HasPrefix(line, "ListenStream=") {
				val := strings.TrimPrefix(line, "ListenStream=")
				// Could be just a port number or host:port
				val = strings.TrimSpace(val)
				if port := extractPort(val); port > 0 {
					return port
				}
			}
		}
	}

	// Check ExecStart for common --port or -p flags
	res = a.transport.ExecuteUnsafe(ctx,
		fmt.Sprintf("systemctl --user show %s --property=ExecStart 2>/dev/null", name))
	if res.Success {
		stdout := res.Stdout
		// Look for --port=NNNN or -p NNNN patterns
		for _, pattern := range []string{"--port=", "--port ", "-p ", ":port="} {
			if idx := strings.Index(stdout, pattern); idx >= 0 {
				numStart := idx + len(pattern)
				numStr := ""
				for i := numStart; i < len(stdout) && i < numStart+6; i++ {
					if stdout[i] >= '0' && stdout[i] <= '9' {
						numStr += string(stdout[i])
					} else {
						break
					}
				}
				if port, err := strconv.Atoi(numStr); err == nil && port > 0 && port < 65536 {
					return port
				}
			}
		}
	}
	return 0
}

// extractPort parses a port from a ListenStream value.
func extractPort(val string) int {
	if val == "" {
		return 0
	}
	// Try direct port number
	if port, err := strconv.Atoi(val); err == nil && port > 0 && port < 65536 {
		return port
	}
	// Try host:port
	if idx := strings.LastIndex(val, ":"); idx >= 0 {
		if port, err := strconv.Atoi(val[idx+1:]); err == nil && port > 0 && port < 65536 {
			return port
		}
	}
	return 0
}

// isSystemService filters out well-known system services from user listing.
func isSystemService(name string) bool {
	skip := []string{
		"dbus", "at-spi", "pipewire", "pulseaudio", "xdg",
		"gvfs", "gnome", "evolution", "tracker", "dconf",
	}
	lower := strings.ToLower(name)
	for _, s := range skip {
		if strings.HasPrefix(lower, s) {
			return true
		}
	}
	return false
}
