package engine

import (
	"context"
	"fmt"
	"strings"
)

// GetScheduledTasks returns cron jobs, systemd timers, and at jobs.
func (a *ServerAgent) GetScheduledTasks(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Scheduled Tasks\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	// /etc/crontab
	res := a.transport.ExecuteUnsafe(ctx, "cat /etc/crontab 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("System crontab (/etc/crontab):\n")
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		b.WriteString("\n")
	}

	// /etc/cron.d/*
	res = a.transport.ExecuteUnsafe(ctx, "ls /etc/cron.d/ 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("Drop-in cron files (/etc/cron.d/):\n")
		for _, file := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			fmt.Fprintf(&b, "  [file] %s\n", file)
			fRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("cat /etc/cron.d/%s 2>/dev/null", file))
			if fRes.Success {
				for _, line := range strings.Split(fRes.Stdout, "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "#") {
						fmt.Fprintf(&b, "    %s\n", line)
					}
				}
			}
		}
		b.WriteString("\n")
	}

	// Per-user crontabs
	res = a.transport.ExecuteUnsafe(ctx, "cut -d: -f1 /etc/passwd 2>/dev/null")
	if res.Success {
		found := false
		for _, user := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			user = strings.TrimSpace(user)
			if user == "" {
				continue
			}
			uRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("crontab -l -u %s 2>/dev/null", user))
			if uRes.Success && strings.TrimSpace(uRes.Stdout) != "" && !strings.Contains(uRes.Stdout, "no crontab for") {
				if !found {
					b.WriteString("User crontabs:\n")
					found = true
				}
				fmt.Fprintf(&b, "  [%s]\n", user)
				for _, line := range strings.Split(uRes.Stdout, "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "#") {
						fmt.Fprintf(&b, "    %s\n", line)
					}
				}
			}
		}
		if found {
			b.WriteString("\n")
		}
	}

	// Systemd timers (system + user)
	res = a.transport.ExecuteUnsafe(ctx, "systemctl list-timers --all --no-pager 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("Systemd timers (system):\n")
		b.WriteString(res.Stdout)
		b.WriteString("\n")
	}
	res = a.transport.ExecuteUnsafe(ctx, "systemctl --user list-timers --all --no-pager 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("Systemd timers (user):\n")
		b.WriteString(res.Stdout)
		b.WriteString("\n")
	}

	// at jobs
	res = a.transport.ExecuteUnsafe(ctx, "atq 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("At jobs:\n")
		b.WriteString(res.Stdout)
		b.WriteString("\n")
	}

	return b.String()
}
