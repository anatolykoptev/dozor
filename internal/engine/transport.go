package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Transport executes commands locally or via SSH.
type Transport struct {
	cfg Config
}

// NewTransport creates a transport from config.
func NewTransport(cfg Config) *Transport {
	return &Transport{cfg: cfg}
}

// Execute runs a command with validation.
func (t *Transport) Execute(ctx context.Context, command string) CommandResult {
	if !t.cfg.IsLocal() {
		// Remote: validation happens on the calling side
		return t.executeSSH(ctx, command)
	}
	return t.executeLocal(ctx, command)
}

// ExecuteUnsafe runs a command without validation (for internal docker commands).
func (t *Transport) ExecuteUnsafe(ctx context.Context, command string) CommandResult {
	if !t.cfg.IsLocal() {
		return t.executeSSH(ctx, command)
	}
	return t.executeLocal(ctx, command)
}

// DockerCommand runs a docker command.
func (t *Transport) DockerCommand(ctx context.Context, dockerCmd string) CommandResult {
	return t.ExecuteUnsafe(ctx, "docker "+dockerCmd)
}

// DockerComposeCommand runs a docker compose command in the compose path.
func (t *Transport) DockerComposeCommand(ctx context.Context, composeCmd string) CommandResult {
	path := t.cfg.ComposePath
	if strings.HasPrefix(path, "~") {
		path = "$HOME" + path[1:]
	}
	cmd := fmt.Sprintf("cd %s && docker compose %s", path, composeCmd)
	return t.ExecuteUnsafe(ctx, cmd)
}

// TestConnection verifies connectivity.
func (t *Transport) TestConnection(ctx context.Context) (bool, string) {
	res := t.ExecuteUnsafe(ctx, "echo connection_ok")
	if res.Success && strings.Contains(res.Stdout, "connection_ok") {
		return true, "connected"
	}
	return false, res.Stderr
}

func (t *Transport) executeLocal(ctx context.Context, command string) CommandResult {
	cmdCtx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 3 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	rc := 0
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return CommandResult{
				Stderr:     fmt.Sprintf("command timed out after %v", t.cfg.Timeout),
				ReturnCode: -1,
				Command:    command,
				Success:    false,
			}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else {
			rc = 1
		}
	}

	return CommandResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ReturnCode: rc,
		Command:    command,
		Success:    rc == 0,
	}
}

func (t *Transport) executeSSH(ctx context.Context, command string) CommandResult {
	cmdCtx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	args := []string{
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if t.cfg.SSHPort != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", t.cfg.SSHPort))
	}
	args = append(args, t.cfg.Host, command)

	cmd := exec.CommandContext(cmdCtx, "ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 3 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	rc := 0
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return CommandResult{
				Stderr:     fmt.Sprintf("SSH command timed out after %v", t.cfg.Timeout),
				ReturnCode: -1,
				Command:    command,
				Success:    false,
			}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else {
			rc = 1
		}
	}

	return CommandResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ReturnCode: rc,
		Command:    command,
		Success:    rc == 0,
	}
}
