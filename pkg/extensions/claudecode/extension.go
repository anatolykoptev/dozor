package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	defaultBinary       = "claude"
	defaultTimeout      = 5 * time.Minute
	defaultAllowedTools = "mcp__dozor__*,Read,Edit,Write,Bash,Glob,Grep"
	maxOutput           = 30000
	// asyncTitleMaxLen is the maximum length of an auto-generated async task title.
	asyncTitleMaxLen = 120
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
	if timeout > 15*time.Minute {
		timeout = 15 * time.Minute
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

	// Build MCP self-connect URL from DOZOR_MCP_PORT (default 8765).
	mcpURL := ""
	mcpEnabled := os.Getenv("DOZOR_CLAUDE_MCP_ENABLED")
	if mcpEnabled != "false" {
		port := os.Getenv("DOZOR_MCP_PORT")
		if port == "" {
			port = "8765"
		}
		mcpURL = "http://127.0.0.1:" + port + "/mcp"
	}

	allowedTools := os.Getenv("DOZOR_CLAUDE_ALLOWED_TOOLS")
	if allowedTools == "" {
		allowedTools = defaultAllowedTools
	}

	t := &claudeCodeTool{
		binary:       binary,
		timeout:      timeout,
		mcpURL:       mcpURL,
		allowedTools: allowedTools,
		notify:       extCtx.Notify,
	}
	extCtx.Tools.Register(t)

	slog.Info("claude_code extension loaded",
		slog.String("binary", binary),
		slog.Duration("timeout", timeout),
		slog.String("mcp_url", mcpURL),
		slog.String("allowed_tools", allowedTools))
	return nil
}

// claudeCodeTool implements toolreg.Tool.
type claudeCodeTool struct {
	binary       string
	timeout      time.Duration
	mcpURL       string
	allowedTools string
	notify       func(string) // sends async notification to admin; may be nil
}

func (t *claudeCodeTool) Name() string { return "claude_code" }
func (t *claudeCodeTool) Description() string {
	return "Delegate a task to Claude Code CLI (runs with full permissions — no user approval needed). " +
		"Claude Code has full access to the local filesystem (read/edit/write), git repos, shell commands, " +
		"and all Dozor server tools (server_inspect, server_exec, server_triage, etc.) " +
		"via the built-in MCP connection. Use for: diagnosing service issues, reading/editing files, " +
		"running builds, git operations, codebase analysis, fixing code. Runs synchronously and returns the result."
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
			"async": map[string]any{
				"type":        "boolean",
				"description": "Run asynchronously: immediately confirms task accepted and sends result via notification when done. Use for long tasks so the user gets an immediate reply.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Short human-readable description shown in the async start notification instead of the full prompt (optional).",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *claudeCodeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("prompt is required")
	}

	async, _ := args["async"].(bool)
	if async && t.notify != nil {
		title, _ := args["title"].(string)
		if strings.TrimSpace(title) == "" {
			if len(prompt) > asyncTitleMaxLen {
				title = prompt[:asyncTitleMaxLen] + "..."
			} else {
				title = prompt
			}
		}
		go func() {
			t.notify("⏳ Task delegated to Claude Code:\n" + title)
			result, err := t.runClaude(context.Background(), prompt, args)
			if err != nil {
				t.notify("❌ Claude Code failed: " + err.Error())
			} else {
				t.notify("✅ Claude Code completed:\n\n" + result)
			}
		}()
		return "Task accepted and delegated to Claude Code in async mode. Result will be sent separately upon completion.", nil
	}

	return t.runClaude(ctx, prompt, args)
}

func (t *claudeCodeTool) runClaude(ctx context.Context, prompt string, args map[string]any) (string, error) {
	cwd, _ := args["cwd"].(string)

	cmdCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// Build CLI args.
	cmdArgs := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}

	// MCP self-connect: write temp config and pass it.
	if t.mcpURL != "" {
		cfgPath, cleanup, err := writeMCPConfig(t.mcpURL)
		if err != nil {
			slog.Warn("claude_code: failed to write MCP config, running without it",
				slog.Any("error", err))
		} else {
			defer cleanup()
			cmdArgs = append(cmdArgs, "--mcp-config", cfgPath)
		}
	}

	// Allowed tools.
	if t.allowedTools != "" {
		for _, tool := range strings.Split(t.allowedTools, ",") {
			tool = strings.TrimSpace(tool)
			if tool != "" {
				cmdArgs = append(cmdArgs, "--allowedTools", tool)
			}
		}
	}

	cmd := exec.CommandContext(cmdCtx, t.binary, cmdArgs...) //nolint:gosec // binary path is validated at startup; args are MCP inputs not user shell input
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
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude_code timed out after %s", t.timeout)
		}
		errOut := strings.TrimSpace(stderr.String())
		if errOut != "" {
			return "", fmt.Errorf("claude_code failed: %w: %s", err, errOut)
		}
		return "", fmt.Errorf("claude_code failed: %w", err)
	}

	out := parseStreamJSON(stdout.String())
	if out == "" {
		out = "(no output)"
	}
	if len(out) > maxOutput {
		out = out[:maxOutput] + fmt.Sprintf("\n... (truncated, %d more chars)", len(out)-maxOutput)
	}
	return out, nil
}

// writeMCPConfig writes a temporary MCP config JSON file pointing to Dozor's own MCP server.
// Returns the file path and a cleanup func. Caller must call cleanup() when done.
func writeMCPConfig(mcpURL string) (string, func(), error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"dozor": map[string]any{
				"type": "http",
				"url":  mcpURL,
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("marshal MCP config: %w", err)
	}

	f, err := os.CreateTemp("", "dozor-mcp-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("create temp MCP config: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("write MCP config: %w", err)
	}
	f.Close()

	path := f.Name()
	cleanup := func() { os.Remove(path) }
	return path, cleanup, nil
}

// parseStreamJSON extracts the final text output from Claude's stream-json format.
// Each line is a JSON event; we collect text from assistant messages and the result.
func parseStreamJSON(raw string) string {
	var parts []string

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event["type"] {
		case "assistant":
			// Extract text blocks from message.content array.
			msg, _ := event["message"].(map[string]any)
			if msg == nil {
				continue
			}
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, _ := block.(map[string]any)
				if b == nil {
					continue
				}
				if b["type"] == "text" {
					if text, ok := b["text"].(string); ok && strings.TrimSpace(text) != "" {
						parts = append(parts, strings.TrimSpace(text))
					}
				}
			}

		case "result":
			// Final result message — use result field if no assistant text collected.
			if len(parts) == 0 {
				if result, ok := event["result"].(string); ok && strings.TrimSpace(result) != "" {
					parts = append(parts, strings.TrimSpace(result))
				}
			}
		}
	}

	if len(parts) == 0 {
		// Fallback: return raw output stripped of JSON lines.
		return strings.TrimSpace(raw)
	}
	return strings.Join(parts, "\n\n")
}

// Ensure interfaces are satisfied.
var _ extensions.Extension = (*Extension)(nil)
var _ toolreg.Tool = (*claudeCodeTool)(nil)
