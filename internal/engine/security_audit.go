package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var stackTracePatterns = []*regexp.Regexp{
	regexp.MustCompile(`Traceback \(most recent call last\)`),
	regexp.MustCompile(`at .+\(.+:\d+:\d+\)`),
	regexp.MustCompile(`File ".+", line \d+`),
	regexp.MustCompile(`Exception in thread`),
}

func (c *SecurityCollector) checkNetworkExposure(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	res := c.transport.ExecuteUnsafe(ctx, "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null")
	if !res.Success {
		return issues
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		for port, svcName := range c.cfg.InternalPorts {
			if strings.Contains(line, "0.0.0.0:"+port) || strings.Contains(line, ":::"+port) {
				issues = append(issues, SecurityIssue{
					Level:       AlertCritical,
					Category:    "network",
					Title:       fmt.Sprintf("%s port %s exposed to all interfaces", svcName, port),
					Description: fmt.Sprintf("Port %s (%s) is bound to 0.0.0.0, accessible from any network", port, svcName),
					Remediation: fmt.Sprintf("Bind %s to 127.0.0.1:%s in docker-compose.yml", svcName, port),
					Evidence:    strings.TrimSpace(line),
				})
			}
		}
	}

	res = c.transport.ExecuteUnsafe(ctx, "ufw status 2>/dev/null")
	if res.Success && strings.Contains(res.Stdout, "inactive") {
		issues = append(issues, SecurityIssue{
			Level:       AlertWarning,
			Category:    "firewall",
			Title:       "Firewall is inactive",
			Description: "UFW firewall is not enabled on this server",
			Remediation: "Enable UFW with: ufw enable (ensure SSH is allowed first)",
		})
	}

	return issues
}

func (c *SecurityCollector) checkContainerSecurity(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	res := c.transport.DockerCommand(ctx, "ps --format '{{.Names}}'")
	if !res.Success {
		return issues
	}

	for _, name := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		userRes := c.transport.DockerCommand(ctx, fmt.Sprintf("exec %s whoami 2>/dev/null", name))
		if userRes.Success && strings.TrimSpace(userRes.Stdout) == "root" && !c.containerRootAllowed(name) {
			issues = append(issues, SecurityIssue{
				Level:       AlertWarning,
				Category:    "container",
				Title:       fmt.Sprintf("Container %s running as root", name),
				Description: "Running as root increases attack surface if container is compromised",
				Remediation: "Add USER directive in Dockerfile or user: in docker-compose.yml",
			})
		}
	}

	// Check for dangerous host mounts
	res = c.transport.DockerCommand(ctx, "inspect --format '{{range .Mounts}}{{.Source}}:{{.Destination}} {{end}}' $(docker ps -q) 2>/dev/null")
	if res.Success {
		for _, mount := range c.cfg.DangerousHostMounts {
			if strings.Contains(res.Stdout, mount) {
				issues = append(issues, SecurityIssue{
					Level:       AlertCritical,
					Category:    "container",
					Title:       "Dangerous host mount: " + mount,
					Description: "Host path " + mount + " is mounted into a container",
					Remediation: "Remove the mount or use a read-only mount (:ro)",
				})
			}
		}
	}

	return issues
}

func (c *SecurityCollector) checkAuthentication(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	if len(c.cfg.RequiredAuthVars) == 0 {
		return issues
	}

	res := c.transport.DockerComposeCommand(ctx, "config 2>/dev/null")
	if !res.Success {
		return issues
	}

	for _, v := range c.cfg.RequiredAuthVars {
		if !strings.Contains(res.Stdout, v) {
			issues = append(issues, SecurityIssue{
				Level:       AlertWarning,
				Category:    "authentication",
				Title:       "Missing auth config: " + v,
				Description: "Environment variable " + v + " not found in compose config",
				Remediation: "Add " + v + " to your .env or compose environment",
			})
		}
	}

	return issues
}

func (c *SecurityCollector) checkAPIHardening(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	services := c.resolveServices(ctx)
	if len(services) == 0 {
		return issues
	}

	for _, svc := range services {
		res := c.transport.DockerComposeCommand(ctx, fmt.Sprintf("logs --tail 50 %s 2>&1", svc))
		if !res.Success {
			continue
		}
		for _, p := range stackTracePatterns {
			if p.MatchString(res.Stdout) {
				issues = append(issues, SecurityIssue{
					Level:       AlertWarning,
					Category:    "api_hardening",
					Title:       fmt.Sprintf("Stack trace exposed in %s logs", svc),
					Description: "Stack traces in logs can reveal internal implementation details",
					Remediation: "Configure error handling to hide stack traces in production",
				})
				break
			}
		}
	}

	return issues
}

func (c *SecurityCollector) checkReconnaissance(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue
	botIndicators := []string{
		"/wp-admin", "/wp-login", "/phpmyadmin", "/.env", "/.git",
		"/xmlrpc.php", "/shell", "/eval", "/exec", "/cgi-bin",
		"/actuator", "/swagger", "/sdk/",
	}

	for _, svc := range c.resolveServices(ctx) {
		if !strings.Contains(svc, "gateway") && !strings.Contains(svc, "proxy") &&
			!strings.Contains(svc, "caddy") && !strings.Contains(svc, "traefik") &&
			!strings.Contains(svc, "nginx") {
			continue
		}

		res := c.transport.DockerComposeCommand(ctx, fmt.Sprintf("logs --tail 500 %s 2>&1", svc))
		if !res.Success {
			continue
		}

		botCount := 0
		for _, line := range strings.Split(res.Stdout, "\n") {
			for _, indicator := range botIndicators {
				if strings.Contains(line, indicator) {
					botCount++
					break
				}
			}
		}

		if botCount > 10 {
			issues = append(issues, SecurityIssue{
				Level:       AlertInfo,
				Category:    "reconnaissance",
				Title:       fmt.Sprintf("Bot scanner activity detected in %s (%d requests)", svc, botCount),
				Description: "Automated vulnerability scanners probing your server",
				Remediation: "Consider adding fail2ban or rate limiting rules",
			})
		}
	}

	return issues
}
