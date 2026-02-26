package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EnvVarStatus describes the status of one environment variable.
type EnvVarStatus struct {
	Name      string
	Value     string
	Empty     bool
	Missing   bool
	Sensitive bool
}

// sensitivePatterns flags env vars that likely contain secrets.
var sensitivePatterns = []string{
	"_KEY", "_SECRET", "_TOKEN", "_PASSWORD", "_PASS", "_PWD",
	"_AUTH", "_CREDENTIAL", "_CERT", "_PRIVATE",
}

func isSensitive(name string) bool {
	upper := strings.ToUpper(name)
	for _, pat := range sensitivePatterns {
		if strings.Contains(upper, pat) {
			return true
		}
	}
	return false
}

// CheckContainerEnv inspects a container's environment variables.
func (a *ServerAgent) CheckContainerEnv(ctx context.Context, service string, required []string) string {
	// Get container name from docker compose
	res := a.transport.DockerComposeCommand(ctx, fmt.Sprintf("ps -q %s 2>/dev/null", service))
	containerID := ""
	if res.Success {
		containerID = strings.TrimSpace(res.Stdout)
	}

	if containerID == "" {
		// Try to find by service name directly
		res = a.transport.DockerCommand(ctx, fmt.Sprintf("ps --filter name=%s --format {{.ID}} 2>/dev/null", service))
		if res.Success {
			lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
			if len(lines) > 0 {
				containerID = strings.TrimSpace(lines[0])
			}
		}
	}

	if containerID == "" {
		return fmt.Sprintf("Container for service %q not found or not running.\n", service)
	}

	// Get environment via docker inspect
	inspectRes := a.transport.DockerCommand(ctx, fmt.Sprintf("inspect --format '{{json .Config.Env}}' %s 2>/dev/null", containerID))
	if !inspectRes.Success {
		return fmt.Sprintf("Failed to inspect container %s: %s\n", containerID, inspectRes.Output())
	}

	var envList []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(inspectRes.Stdout)), &envList); err != nil {
		return fmt.Sprintf("Failed to parse env for container %s\n", containerID)
	}

	// Build env map
	envMap := make(map[string]string)
	for _, kv := range envList {
		idx := strings.Index(kv, "=")
		if idx < 0 {
			envMap[kv] = ""
		} else {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Environment Variables for %s (%d total)\n\n", service, len(envMap))

	// Required vars first
	if len(required) > 0 {
		b.WriteString("Required variables:\n")
		for _, name := range required {
			val, exists := envMap[name]
			sensitive := isSensitive(name)
			switch {
			case !exists:
				fmt.Fprintf(&b, "  [MISSING] %s\n", name)
			case val == "":
				fmt.Fprintf(&b, "  [EMPTY]   %s\n", name)
			case sensitive:
				fmt.Fprintf(&b, "  [OK]      %s = <redacted>\n", name)
			default:
				fmt.Fprintf(&b, "  [OK]      %s = %s\n", name, val)
			}
		}
		b.WriteString("\n")
	}

	// Sensitive vars check (default values / empty)
	b.WriteString("Sensitive variables (auto-detected):\n")
	found := false
	for name, val := range envMap {
		if !isSensitive(name) {
			continue
		}
		found = true
		switch {
		case val == "":
			fmt.Fprintf(&b, "  [EMPTY]   %s — WARNING: empty secret\n", name)
		case isDefaultValue(val):
			fmt.Fprintf(&b, "  [DEFAULT] %s = %q — WARNING: looks like a default/example value\n", name, val)
		default:
			fmt.Fprintf(&b, "  [SET]     %s = <redacted>\n", name)
		}
	}
	if !found {
		b.WriteString("  (none detected)\n")
	}

	return b.String()
}

// isDefaultValue checks for common placeholder/example secret values.
func isDefaultValue(val string) bool {
	lower := strings.ToLower(val)
	defaults := []string{
		"changeme", "secret", "password", "pass", "example",
		"your_", "replace_", "xxx", "123456", "test", "demo",
	}
	for _, d := range defaults {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}
