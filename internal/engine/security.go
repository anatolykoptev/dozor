package engine

import (
	"context"
	"fmt"
	"strings"
)

const (
	// firewallEvidenceMaxLen is the max length for firewall evidence snippets.
	firewallEvidenceMaxLen = 500
	// cronEvidenceMaxLen is the max length for cron evidence snippets.
	cronEvidenceMaxLen = 800
	// permFieldsMin is the minimum field count when checking file permissions.
	permFieldsMin = 3
	// firewallAcceptWarnCount is the number of blanket ACCEPT iptables rules triggering a warning.
	firewallAcceptWarnCount = 3
)

// SecurityCollector runs all security checks.
type SecurityCollector struct {
	transport *Transport
	cfg       Config
	discovery *DockerDiscovery
}

// resolveServices returns configured services or falls back to discovery.
func (c *SecurityCollector) resolveServices(ctx context.Context) []string {
	if len(c.cfg.Services) > 0 {
		return c.cfg.Services
	}
	if c.discovery != nil {
		return c.discovery.DiscoverServices(ctx)
	}
	return nil
}

// CheckAll runs all security checks and returns issues.
func (c *SecurityCollector) CheckAll(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	issues = append(issues, c.checkNetworkExposure(ctx)...)
	issues = append(issues, c.checkContainerSecurity(ctx)...)
	issues = append(issues, c.checkAuthentication(ctx)...)
	issues = append(issues, c.checkAPIHardening(ctx)...)
	issues = append(issues, c.checkReconnaissance(ctx)...)
	issues = append(issues, c.checkUpstreamVulns(ctx)...)
	issues = append(issues, c.checkFirewallRules(ctx)...)
	issues = append(issues, c.checkCrontabs(ctx)...)
	issues = append(issues, c.checkActiveConnections(ctx)...)
	return issues
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}

// containerRootAllowed returns true if name matches any key in cfg.RootAllowedContainers.
func (c *SecurityCollector) containerRootAllowed(name string) bool {
	if c.cfg.RootAllowedContainers[name] {
		return true
	}
	for container := range c.cfg.RootAllowedContainers {
		if strings.Contains(name, container) {
			return true
		}
	}
	return false
}

// appendNonCommentLines appends non-empty, non-comment lines with optional prefix to a builder.
// Returns the count of lines appended.
func appendNonCommentLines(b *strings.Builder, text, prefix string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		count++
		if prefix != "" {
			fmt.Fprintf(b, "[%s] %s\n", prefix, line)
		} else {
			b.WriteString(line + "\n")
		}
	}
	return count
}

// parseCountField parses the count from the first field of a `uniq -c` output line.
func parseCountField(fields []string) int {
	if len(fields) < 2 {
		return 0
	}
	count := 0
	_, _ = fmt.Sscanf(fields[0], "%d", &count)
	return count
}

func (c *SecurityCollector) checkDockerSocket(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue

	res := c.transport.ExecuteUnsafe(ctx, "ls -la /var/run/docker.sock 2>/dev/null")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return issues
	}

	line := strings.TrimSpace(res.Stdout)
	// Check if world-writable (srw-rw-rw- or srwxrwxrwx)
	if strings.HasPrefix(line, "srw-rw-rw") || strings.HasPrefix(line, "srwxrwxrwx") {
		issues = append(issues, SecurityIssue{
			Level:       AlertCritical,
			Category:    "container",
			Title:       "Docker socket is world-writable",
			Description: "Any user on the system can control Docker, enabling full host privilege escalation",
			Remediation: "Run: sudo chmod 660 /var/run/docker.sock; ensure only docker group members need access",
		})
	}

	return issues
}

func (c *SecurityCollector) checkZombieProcesses(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue

	res := c.transport.ExecuteUnsafe(ctx, "ps aux 2>/dev/null | awk '$8 == \"Z\" {print $0}' | grep -v 'STAT'")
	if !res.Success {
		return issues
	}

	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	count := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			count++
		}
	}

	if count > 0 {
		issues = append(issues, SecurityIssue{
			Level:       AlertInfo,
			Category:    "process",
			Title:       fmt.Sprintf("%d zombie process(es) detected", count),
			Description: "Zombie processes are not reclaimed by their parent, indicating a bug in the parent process",
			Remediation: "Identify the parent process (ppid) and restart it if zombie count grows",
		})
	}

	return issues
}

func (c *SecurityCollector) checkActiveConnections(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue

	res := c.transport.ExecuteUnsafe(ctx, "ss -tn state established 2>/dev/null | awk 'NR>1{split($4,a,\":\"); print a[1]}' | sort | uniq -c | sort -rn | head -10")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			fields := strings.Fields(line)
			count := parseCountField(fields)
			if count > connIPHighWarn {
				issues = append(issues, SecurityIssue{
					Level:       AlertWarning,
					Category:    "network",
					Title:       fmt.Sprintf("IP %s has %d active connections", fields[1], count),
					Description: "Single IP with unusually high connection count may indicate abuse or misconfigured client",
					Remediation: "Investigate the source IP; consider rate limiting or firewall rules",
				})
			}
		}
	}

	res = c.transport.ExecuteUnsafe(ctx, "ss -tn 2>/dev/null | awk 'NR>1{print $1}' | sort | uniq -c | sort -rn")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			fields := strings.Fields(line)
			count := parseCountField(fields)
			if len(fields) >= 2 && strings.EqualFold(fields[1], "CLOSE-WAIT") && count > connCloseWaitWarn {
				issues = append(issues, SecurityIssue{
					Level:       AlertWarning,
					Category:    "network",
					Title:       fmt.Sprintf("%d connections in CLOSE-WAIT state", count),
					Description: "High CLOSE-WAIT count indicates connection leak — application not closing sockets properly",
					Remediation: "Identify the leaking service and fix socket cleanup in application code",
				})
			}
		}
	}

	return issues
}
