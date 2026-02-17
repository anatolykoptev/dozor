package engine

import (
	"context"
	"fmt"
	"strings"
)

// GetSystemdStatus returns status of local systemd services.
func (a *ServerAgent) GetSystemdStatus(ctx context.Context, services []string) string {
	if len(services) == 0 {
		services = a.cfg.SystemdServices
	}
	if len(services) == 0 {
		return "No systemd services configured. Set DOZOR_SYSTEMD_SERVICES in .env."
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
