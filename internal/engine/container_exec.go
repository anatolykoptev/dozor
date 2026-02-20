package engine

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

const containerExecMaxOutput = 50 * 1024 // 50KB

// containerExecBlockedPatterns are commands that must never be run inside containers.
var containerExecBlockedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rm\s+(-r|-f|-rf|--recursive|--force)\s+/`),
	regexp.MustCompile(`(?i):\(\)\s*\{`),                                  // fork bomb
	regexp.MustCompile(`(?i)(bash|sh|zsh)\s+-i`),                          // interactive shell
	regexp.MustCompile(`(?i)curl.*\|\s*(bash|sh|zsh|python|perl)`),        // download-and-exec
	regexp.MustCompile(`(?i)wget.*\|\s*(bash|sh|zsh|python|perl)`),        // download-and-exec
	regexp.MustCompile(`(?i)\bnc\s+-[el]`),                                // netcat listener (reverse shell)
	regexp.MustCompile(`(?i)\bsocat\b.*\bexec\b`),                         // socat exec
	regexp.MustCompile(`(?i)/dev/tcp/`),                                   // bash reverse shell
	regexp.MustCompile(`(?i)mkfs`),                                        // format filesystem
	regexp.MustCompile(`(?i)dd\s+if=.*of=/dev/`),                          // disk overwrite
	regexp.MustCompile(`(?i)\breboot\b|\bshutdown\b|\bhalt\b|\bpoweroff\b`),
}

// IsContainerExecAllowed validates a command against the blocklist.
// Returns (allowed, reason).
func IsContainerExecAllowed(cmd string) (bool, string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false, "empty command"
	}
	for _, p := range containerExecBlockedPatterns {
		if p.MatchString(cmd) {
			return false, fmt.Sprintf("blocked: %s", p.String())
		}
	}
	return true, ""
}

// ContainerExec runs a command inside a container using Docker SDK.
func (a *ServerAgent) ContainerExec(ctx context.Context, containerName, command, user string) (string, error) {
	if a.discovery == nil {
		return "", fmt.Errorf("container exec requires local Docker access (Docker SDK not available)")
	}

	containerID, err := a.resolveContainerID(ctx, containerName)
	if err != nil {
		return "", err
	}

	cli := a.discovery.Client()

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	}
	if user != "" {
		execCfg.User = user
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("exec create failed: %w", err)
	}

	attachResp, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach failed: %w", err)
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader)
	if err != nil {
		return "", fmt.Errorf("reading exec output: %w", err)
	}

	// Get exit code
	inspectResp, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect failed: %w", err)
	}

	// Combine output
	var result strings.Builder
	if stdout.Len() > 0 {
		out := stdout.String()
		if len(out) > containerExecMaxOutput {
			out = out[:containerExecMaxOutput] + "\n... (truncated at 50KB)"
		}
		result.WriteString(out)
	}
	if stderr.Len() > 0 {
		errOut := stderr.String()
		if len(errOut) > containerExecMaxOutput {
			errOut = errOut[:containerExecMaxOutput] + "\n... (truncated at 50KB)"
		}
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("[stderr] ")
		result.WriteString(errOut)
	}

	if inspectResp.ExitCode != 0 {
		result.WriteString(fmt.Sprintf("\n\nExit code: %d", inspectResp.ExitCode))
	}

	return result.String(), nil
}

// resolveContainerID finds a container by name, service name, or partial match.
func (a *ServerAgent) resolveContainerID(ctx context.Context, name string) (string, error) {
	containers := a.discovery.ListContainers(ctx)

	// Exact match on name or service
	for _, c := range containers {
		if c.Name == name || c.Service == name {
			return c.ID, nil
		}
	}

	// Partial match
	var matches []DiscoveredContainer
	for _, c := range containers {
		if strings.Contains(c.Name, name) || strings.Contains(c.Service, name) {
			matches = append(matches, c)
		}
	}

	if len(matches) == 1 {
		return matches[0].ID, nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return "", fmt.Errorf("ambiguous container name %q, matches: %s", name, strings.Join(names, ", "))
	}

	return "", fmt.Errorf("container %q not found", name)
}
