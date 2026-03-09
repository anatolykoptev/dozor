package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var suspiciousCronRe = regexp.MustCompile(`(?i)(curl|wget).*\|\s*(bash|sh|zsh|python|perl)|base64\s+-d|/dev/tcp/`)

func (c *SecurityCollector) checkUpstreamVulns(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	res := c.transport.ExecuteUnsafe(ctx, "apt list --upgradable 2>/dev/null | grep -i security")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
		count := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				count++
			}
		}
		if count > 0 {
			issues = append(issues, SecurityIssue{
				Level:       AlertWarning,
				Category:    "packages",
				Title:       fmt.Sprintf("%d security package update(s) available", count),
				Description: fmt.Sprintf("apt reports %d upgradable packages with security fixes", count),
				Remediation: "Run: sudo apt update && sudo apt upgrade -y",
			})
		}
	}

	issues = append(issues, c.checkSSHConfig(ctx)...)
	issues = append(issues, c.checkEnvFilePermissions(ctx)...)
	issues = append(issues, c.checkDockerSocket(ctx)...)
	issues = append(issues, c.checkZombieProcesses(ctx)...)

	return issues
}

func (c *SecurityCollector) checkSSHConfig(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	res := c.transport.ExecuteUnsafe(ctx, "sshd -T 2>/dev/null")
	if !res.Success {
		return issues
	}

	config := strings.ToLower(res.Stdout)
	if strings.Contains(config, "permitrootlogin yes") {
		issues = append(issues, SecurityIssue{
			Level:       AlertWarning,
			Category:    "ssh",
			Title:       "SSH PermitRootLogin is enabled",
			Description: "Direct SSH login as root is allowed",
			Remediation: "Set PermitRootLogin no in /etc/ssh/sshd_config and restart sshd",
		})
	}
	if strings.Contains(config, "passwordauthentication yes") {
		issues = append(issues, SecurityIssue{
			Level:       AlertWarning,
			Category:    "ssh",
			Title:       "SSH password authentication is enabled",
			Description: "SSH allows password-based authentication (brute-force risk)",
			Remediation: "Set PasswordAuthentication no in /etc/ssh/sshd_config; use SSH keys only",
		})
	}
	if strings.Contains(config, "permitemptypasswords yes") {
		issues = append(issues, SecurityIssue{
			Level:       AlertCritical,
			Category:    "ssh",
			Title:       "SSH allows empty passwords",
			Description: "Accounts with empty passwords can log in via SSH",
			Remediation: "Set PermitEmptyPasswords no in /etc/ssh/sshd_config",
		})
	}

	return issues
}

func (c *SecurityCollector) checkEnvFilePermissions(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	res := c.transport.ExecuteUnsafe(ctx, `find ~ /opt /srv 2>/dev/null -maxdepth 4 -name ".env" -o -name ".env.*" 2>/dev/null | head -20`)
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return issues
	}

	for _, path := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		permRes := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf("stat -c '%%a' %s 2>/dev/null", path))
		if !permRes.Success {
			continue
		}
		perm := strings.TrimSpace(permRes.Stdout)
		if len(perm) >= permFieldsMin {
			groupPerm := string(perm[len(perm)-2])
			worldPerm := string(perm[len(perm)-1])
			if worldPerm != "0" || groupPerm == "4" || groupPerm == "6" || groupPerm == "7" {
				issues = append(issues, SecurityIssue{
					Level:       AlertWarning,
					Category:    "files",
					Title:       fmt.Sprintf(".env file has permissive permissions (%s): %s", perm, path),
					Description: "Secrets file is readable by group or world",
					Remediation: "Run: chmod 600 " + path,
					Evidence:    path,
				})
			}
		}
	}

	return issues
}

func (c *SecurityCollector) checkFirewallRules(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	res := c.transport.ExecuteUnsafe(ctx, "iptables -L -n --line-numbers 2>/dev/null | head -60")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		acceptCount := 0
		for _, line := range strings.Split(res.Stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "ACCEPT" {
				acceptCount++
			}
		}
		if acceptCount > firewallAcceptWarnCount {
			issues = append(issues, SecurityIssue{
				Level:       AlertWarning,
				Category:    "firewall",
				Title:       fmt.Sprintf("iptables has %d blanket ACCEPT rules", acceptCount),
				Description: "Multiple blanket ACCEPT rules may indicate overly permissive firewall",
				Remediation: "Review iptables rules; restrict ACCEPT to specific ports/IPs",
				Evidence:    truncate(res.Stdout, firewallEvidenceMaxLen),
			})
		}
	}

	res = c.transport.ExecuteUnsafe(ctx, "nft list ruleset 2>/dev/null | head -40")
	if res.Success {
		output := strings.TrimSpace(res.Stdout)
		if output == "" {
			issues = append(issues, SecurityIssue{
				Level:       AlertInfo,
				Category:    "firewall",
				Title:       "nftables ruleset is empty",
				Description: "No nftables rules configured (may be using iptables instead)",
				Remediation: "Verify firewall is managed via iptables or UFW if nftables is not in use",
			})
		}
	}

	return issues
}

func (c *SecurityCollector) collectCronLines(ctx context.Context) (int, string) {
	var allCrons strings.Builder
	cronCount := 0
	res := c.transport.ExecuteUnsafe(ctx, "cat /etc/crontab 2>/dev/null")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		cronCount += appendNonCommentLines(&allCrons, res.Stdout, "")
	}

	res = c.transport.ExecuteUnsafe(ctx, "cut -d: -f1 /etc/passwd 2>/dev/null")
	if !res.Success {
		return cronCount, allCrons.String()
	}
	for _, user := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		uRes := c.transport.ExecuteUnsafe(ctx, "crontab -l -u "+user+" 2>/dev/null")
		if !uRes.Success || strings.TrimSpace(uRes.Stdout) == "" || strings.Contains(uRes.Stdout, "no crontab for") {
			continue
		}
		cronCount += appendNonCommentLines(&allCrons, uRes.Stdout, user)
	}
	return cronCount, allCrons.String()
}

func (c *SecurityCollector) checkCrontabs(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	cronCount, cronText := c.collectCronLines(ctx)
	if cronCount > 0 {
		issues = append(issues, SecurityIssue{
			Level:       AlertInfo,
			Category:    "cron",
			Title:       fmt.Sprintf("%d cron job(s) found", cronCount),
			Description: "Active cron jobs on the system",
			Remediation: "Review cron jobs for unneeded or suspicious entries",
			Evidence:    truncate(cronText, cronEvidenceMaxLen),
		})
	}

	if suspiciousCronRe.MatchString(cronText) {
		issues = append(issues, SecurityIssue{
			Level:       AlertCritical,
			Category:    "cron",
			Title:       "Suspicious cron job pattern detected",
			Description: "Cron job contains curl/wget piped to shell, base64 decoding, or /dev/tcp usage",
			Remediation: "Investigate immediately; these patterns are common in malware persistence",
		})
	}

	return issues
}
