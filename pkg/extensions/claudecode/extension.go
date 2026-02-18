package claudecode

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/anatolykoptev/dozor/pkg/extensions"
)

const (
	defaultBinary  = "claude"
	defaultTimeout = 2 * time.Minute
	maxOutput      = 30000
)

// Extension registers the claude_code tool into Dozor's agent tool registry.
type Extension struct{}

func New() *Extension { return &Extension{} }

func (e *Extension) Name() string { return "claude_code" }

func (e *Extension) Register(_ context.Context, extCtx *extensions.Context) error {
	binary := strings.TrimSpace(os.Getenv("DOZOR_CLAUDE_BINARY"))
	if binary == "" {
		binary = defaultBinary
	}

	timeout := defaultTimeout
	if s := os.Getenv("DOZOR_CLAUDE_TIMEOUT_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}

	// Verify binary exists before registering.
	if _, err := exec.LookPath(binary); err != nil {
		slog.Warn("claude_code extension: binary not found, skipping",
			slog.String("binary", binary))
		return nil
	}

	if extCtx.Tools == nil {
		slog.Warn("claude_code extension: no tool registry available, skipping")
		return nil
	}

	t := &claudeCodeTool{binary: binary, timeout: timeout}
	extCtx.Tools.Register(t)

	slog.Info("claude_code extension loaded",
		slog.String("binary", binary),
		slog.Duration("timeout", timeout))
	return nil
}

// claudeCodeTool implements toolreg.Tool.
type claudeCodeTool struct {
	binary  string
	timeout time.Duration
}

func (t *claudeCodeTool) Name() string { return "claude_code" }
func (t *claudeCodeTool) Description() string {
	return "Delegate a coding or file-system task to Claude Code CLI. " +
		"Claude Code has full access to the local filesystem, git repos, and dev tools. " +
		"Use for: reading/editing files, running builds, git operations, codebase analysis. " +
		"Runs synchronously and returns the result."
}

func (t *claudeCodeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task or question for Claude Code",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory for the Claude Code session (optional)",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *claudeCodeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}

	cwd, _ := args["cwd"].(string)

	cmdCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	cmdArgs := []string{"-p", "--output-format", "text", prompt}

	cmd := exec.CommandContext(cmdCtx, t.binary, cmdArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude_code timed out after %s", t.timeout)
		}
		errOut := strings.TrimSpace(stderr.String())
		if errOut != "" {
			return "", fmt.Errorf("claude_code failed: %v: %s", err, errOut)
		}
		return "", fmt.Errorf("claude_code failed: %w", err)
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = "(no output)"
	}
	if len(out) > maxOutput {
		out = out[:maxOutput] + fmt.Sprintf("\n... (truncated, %d more chars)", len(out)-maxOutput)
	}
	return out, nil
}

// Ensure interfaces are satisfied.
var _ extensions.Extension = (*Extension)(nil)
var _ toolreg.Tool = (*claudeCodeTool)(nil)
