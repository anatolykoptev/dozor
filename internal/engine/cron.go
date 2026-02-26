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

	writeCronEtc(ctx, &b, a)
	writeCronD(ctx, &b, a)
	writeUserCrontabs(ctx, &b, a)
	writeSystemdTimers(ctx, &b, a)
	writeAtJobs(ctx, &b, a)

	return b.String()
}

// writeCronEtc writes non-comment lines from /etc/crontab.
func writeCronEtc(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "cat /etc/crontab 2>/dev/null")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("System crontab (/etc/crontab):\n")
	writeCronLines(b, res.Stdout, "  ")
	b.WriteString("\n")
}

// writeCronD lists drop-in cron files and their non-comment entries.
func writeCronD(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "ls /etc/cron.d/ 2>/dev/null")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("Drop-in cron files (/etc/cron.d/):\n")
	for _, file := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		fmt.Fprintf(b, "  [file] %s\n", file)
		fRes := a.transport.ExecuteUnsafe(ctx, "cat /etc/cron.d/"+file+" 2>/dev/null")
		if fRes.Success {
			writeCronLines(b, fRes.Stdout, "    ")
		}
	}
	b.WriteString("\n")
}

// writeUserCrontabs lists per-user crontab entries for all system users.
func writeUserCrontabs(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "cut -d: -f1 /etc/passwd 2>/dev/null")
	if !res.Success {
		return
	}
	found := false
	for _, user := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		uRes := a.transport.ExecuteUnsafe(ctx, "crontab -l -u "+user+" 2>/dev/null")
		if !uRes.Success || strings.TrimSpace(uRes.Stdout) == "" || strings.Contains(uRes.Stdout, "no crontab for") {
			continue
		}
		if !found {
			b.WriteString("User crontabs:\n")
			found = true
		}
		fmt.Fprintf(b, "  [%s]\n", user)
		writeCronLines(b, uRes.Stdout, "    ")
	}
	if found {
		b.WriteString("\n")
	}
}

// writeSystemdTimers writes system and user systemd timers.
func writeSystemdTimers(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "systemctl list-timers --all --no-pager 2>/dev/null")
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
}

// writeAtJobs writes pending at(1) jobs.
func writeAtJobs(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "atq 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("At jobs:\n")
		b.WriteString(res.Stdout)
		b.WriteString("\n")
	}
}

// writeCronLines writes non-empty, non-comment lines from a crontab block with given indentation.
func writeCronLines(b *strings.Builder, text, indent string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			fmt.Fprintf(b, "%s%s\n", indent, line)
		}
	}
}
