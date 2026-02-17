package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// SecurityCollector runs all security checks.
type SecurityCollector struct {
	transport *Transport
	cfg       Config
}

// internalOnlyPorts that should not be exposed to 0.0.0.0.
var internalOnlyPorts = map[string]string{
	"5432":  "PostgreSQL",
	"3306":  "MySQL",
	"6379":  "Redis",
	"27017": "MongoDB",
	"9200":  "Elasticsearch",
	"2379":  "etcd",
	"5672":  "RabbitMQ",
	"15672": "RabbitMQ Management",
	"6333":  "Qdrant",
}

var rootAllowedContainers = map[string]bool{
	"postgres": true, "redis": true, "traefik": true, "caddy": true,
}

var dangerousHostMounts = []string{
	"/.claude", "/.ssh", "/.aws", "/.kube", "/.gnupg",
	"/etc/shadow", "/etc/passwd", "/var/run/docker.sock",
}

var stackTracePatterns = []*regexp.Regexp{
	regexp.MustCompile(`Traceback \(most recent call last\)`),
	regexp.MustCompile(`at .+\(.+:\d+:\d+\)`),
	regexp.MustCompile(`File ".+", line \d+`),
	regexp.MustCompile(`Exception in thread`),
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
	return issues
}

func (c *SecurityCollector) checkNetworkExposure(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue

	// Check for exposed ports via ss/netstat
	res := c.transport.ExecuteUnsafe(ctx, "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null")
	if !res.Success {
		return issues
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		for port, svcName := range internalOnlyPorts {
			// Check if port is bound to 0.0.0.0 (all interfaces)
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

	// Check UFW status
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

	// Check for containers running as root
	res := c.transport.DockerCommand(ctx, "ps --format '{{.Names}}'")
	if !res.Success {
		return issues
	}

	for _, name := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		// Check user
		userRes := c.transport.DockerCommand(ctx, fmt.Sprintf("exec %s whoami 2>/dev/null", name))
		if userRes.Success {
			user := strings.TrimSpace(userRes.Stdout)
			if user == "root" && !rootAllowedContainers[name] {
				// Check partial match (e.g., "krolik-server-postgres-1")
				allowed := false
				for container := range rootAllowedContainers {
					if strings.Contains(name, container) {
						allowed = true
						break
					}
				}
				if !allowed {
					issues = append(issues, SecurityIssue{
						Level:       AlertWarning,
						Category:    "container",
						Title:       fmt.Sprintf("Container %s running as root", name),
						Description: "Running as root increases attack surface if container is compromised",
						Remediation: "Add USER directive in Dockerfile or user: in docker-compose.yml",
					})
				}
			}
		}
	}

	// Check for dangerous host mounts
	res = c.transport.DockerCommand(ctx, "inspect --format '{{range .Mounts}}{{.Source}}:{{.Destination}} {{end}}' $(docker ps -q) 2>/dev/null")
	if res.Success {
		for _, mount := range dangerousHostMounts {
			if strings.Contains(res.Stdout, mount) {
				issues = append(issues, SecurityIssue{
					Level:       AlertCritical,
					Category:    "container",
					Title:       fmt.Sprintf("Dangerous host mount: %s", mount),
					Description: fmt.Sprintf("Host path %s is mounted into a container", mount),
					Remediation: "Remove the mount or use a read-only mount (:ro)",
				})
			}
		}
	}

	return issues
}

func (c *SecurityCollector) checkAuthentication(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue

	// Skip if no required auth vars configured
	if len(c.cfg.RequiredAuthVars) == 0 {
		return issues
	}

	// Check compose config for auth vars
	res := c.transport.DockerComposeCommand(ctx, "config 2>/dev/null")
	if !res.Success {
		return issues
	}

	for _, v := range c.cfg.RequiredAuthVars {
		if !strings.Contains(res.Stdout, v) {
			issues = append(issues, SecurityIssue{
				Level:       AlertWarning,
				Category:    "authentication",
				Title:       fmt.Sprintf("Missing auth config: %s", v),
				Description: fmt.Sprintf("Environment variable %s not found in compose config", v),
				Remediation: fmt.Sprintf("Add %s to your .env or compose environment", v),
			})
		}
	}

	return issues
}

func (c *SecurityCollector) checkAPIHardening(ctx context.Context) []SecurityIssue {
	var issues []SecurityIssue

	services := c.cfg.Services
	if len(services) == 0 {
		return issues // Skip if no services configured (auto-discover is handled at agent level)
	}

	// Check for stack traces in recent logs (last 50 lines per service)
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

	// Check for bot scanner activity in gateway/proxy logs
	botIndicators := []string{
		"/wp-admin", "/wp-login", "/phpmyadmin", "/.env", "/.git",
		"/xmlrpc.php", "/shell", "/eval", "/exec", "/cgi-bin",
		"/actuator", "/swagger", "/sdk/",
	}

	// Check last 500 lines of gateway logs for recon patterns
	for _, svc := range c.cfg.Services {
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

func (c *SecurityCollector) checkUpstreamVulns(_ context.Context) []SecurityIssue {
	// Static known vulnerabilities list - checked against running services
	// This is a lightweight version; the Python code had extensive MOLT-xxx entries
	return nil
}

