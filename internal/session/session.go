package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const (
	// progressRateLimit is the minimum interval between tool_use progress notifications.
	progressRateLimit = 500 * time.Millisecond
	// maxMessageLen is the maximum length of a single Telegram message.
	maxMessageLen = 4000
	// scannerMaxBuf is the max token size for the stdout scanner (1 MB).
	scannerMaxBuf = 1024 * 1024
)

// NotifyFn sends a message to the user's chat.
type NotifyFn func(chatID, text string)

// Session wraps a long-running Claude Code process with stream-json I/O.
type Session struct {
	chatID  string
	notify  NotifyFn
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	stdinMu sync.Mutex
	closed  atomic.Bool

	idleTimer   *time.Timer
	idleTimeout time.Duration

	lastProgress   time.Time
	lastProgressMu sync.Mutex
}

// Start spawns a Claude Code process and begins reading its output.
// The initial prompt is sent as the first user message.
// This function returns after the process starts; output is handled by readLoop in a goroutine.
func Start(ctx context.Context, chatID, prompt string, cfg Config, notify NotifyFn) (*Session, error) {
	cmdCtx, cancel := context.WithCancel(ctx)

	args := buildCLIArgs(cfg)

	cmd := exec.CommandContext(cmdCtx, cfg.Binary, args...) //nolint:gosec // binary validated upstream
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	s := &Session{
		chatID:      chatID,
		notify:      notify,
		cancel:      cancel,
		stdin:       stdinPipe,
		idleTimeout: cfg.IdleTimeout,
	}

	s.idleTimer = time.AfterFunc(cfg.IdleTimeout, func() {
		slog.Info("session idle timeout", slog.String("chat_id", chatID))
		s.notifyUser("⏰ Claude Code session closed (idle timeout). Dozor agent active.")
		s.Close()
	})

	// Send initial prompt.
	if err := s.sendMessage(prompt); err != nil {
		cancel()
		return nil, fmt.Errorf("send initial prompt: %w", err)
	}

	// Read loop in background.
	go s.readLoop(stdoutPipe, cmd)

	return s, nil
}

// Send writes a follow-up user message to the Claude Code process.
func (s *Session) Send(text string) {
	if s.closed.Load() {
		return
	}
	s.resetIdle()
	if err := s.sendMessage(text); err != nil {
		slog.Warn("session send failed", slog.String("chat_id", s.chatID), slog.Any("error", err))
		s.notifyUser("⚠️ Failed to send message to session")
	}
}

// Close gracefully shuts down the session.
func (s *Session) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.idleTimer.Stop()
	// Close stdin to signal EOF to Claude Code.
	s.stdinMu.Lock()
	s.stdin.Close()
	s.stdinMu.Unlock()
	// Cancel context as fallback (sends SIGKILL via SysProcAttr).
	s.cancel()
}

// sendMessage writes a stream-json user message to stdin.
func (s *Session) sendMessage(text string) error {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
		"uuid": uuid.NewString(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()

	_, err = s.stdin.Write(data)
	return err
}

// readLoop reads stream-json events from stdout and dispatches them.
func (s *Session) readLoop(stdout io.Reader, cmd *exec.Cmd) {
	defer func() {
		_ = cmd.Wait()
		if !s.closed.Load() {
			s.notifyUser("🔚 Claude Code session ended")
			s.Close()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	// Increase buffer for large outputs.
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxBuf) //nolint:mnd // initial buf size is fine

	for scanner.Scan() {
		if s.closed.Load() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.handleEvent(line)
	}
}

// handleEvent processes a single stream-json event line.
func (s *Session) handleEvent(line string) {
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return
	}

	switch event["type"] {
	case "assistant":
		s.handleAssistant(event)

	case "result":
		s.resetIdle()
		// Check for error subtype.
		if subtype, _ := event["subtype"].(string); subtype == "error" {
			if errMsg, _ := event["error"].(string); errMsg != "" {
				s.notifyUser("❌ Error: " + errMsg)
			}
		}
	}
}

// handleAssistant processes assistant events — sends text blocks and tool progress.
func (s *Session) handleAssistant(event map[string]any) {
	msg, _ := event["message"].(map[string]any)
	if msg == nil {
		return
	}
	content, _ := msg["content"].([]any)

	for _, block := range content {
		b, _ := block.(map[string]any)
		if b == nil {
			continue
		}

		switch b["type"] {
		case "text":
			text, _ := b["text"].(string)
			text = strings.TrimSpace(text)
			if text != "" {
				s.notifyUser(truncate(text, maxMessageLen))
			}

		case "tool_use":
			s.handleToolUse(b)
		}
	}
}

// handleToolUse sends rate-limited tool progress notifications.
func (s *Session) handleToolUse(block map[string]any) {
	s.lastProgressMu.Lock()
	defer s.lastProgressMu.Unlock()

	now := time.Now()
	if now.Sub(s.lastProgress) < progressRateLimit {
		return
	}
	s.lastProgress = now

	name, _ := block["name"].(string)
	input, _ := block["input"].(map[string]any)
	progress := formatToolProgress(name, input)
	if progress != "" {
		s.notifyUser(progress)
	}
}

// notifyUser sends a message to the session's chat.
func (s *Session) notifyUser(text string) {
	if s.notify != nil {
		s.notify(s.chatID, text)
	}
}

// resetIdle resets the idle timer.
func (s *Session) resetIdle() {
	s.idleTimer.Reset(s.idleTimeout)
}

// buildCLIArgs constructs the Claude Code CLI arguments for an interactive session.
func buildCLIArgs(cfg Config) []string {
	args := []string{
		"-p", "--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}

	// MCP self-connect config.
	if cfg.MCPPort != "" {
		mcpURL := "http://127.0.0.1:" + cfg.MCPPort + "/mcp"
		cfgPath, cleanup, err := writeMCPConfig(mcpURL)
		if err != nil {
			slog.Warn("session: failed to write MCP config, running without it",
				slog.Any("error", err))
		} else {
			// Note: temp file will be cleaned up when the process exits.
			// We don't defer cleanup here since we need the file to persist.
			_ = cleanup
			args = append(args, "--mcp-config", cfgPath)
		}
	}

	// Allowed tools.
	if cfg.AllowedTools != "" {
		for _, tool := range strings.Split(cfg.AllowedTools, ",") {
			tool = strings.TrimSpace(tool)
			if tool != "" {
				args = append(args, "--allowedTools", tool)
			}
		}
	}

	return args
}

// writeMCPConfig creates a temporary MCP config for the session.
// It reads ~/.mcp.json to include all user-configured MCP servers,
// and ensures Dozor's own MCP server is always present.
func writeMCPConfig(mcpURL string) (string, func(), error) {
	servers := loadUserMCPServers()

	// Ensure dozor is always present with the correct URL.
	servers["dozor"] = map[string]any{
		"type": "http",
		"url":  mcpURL,
	}

	cfg := map[string]any{"mcpServers": servers}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("marshal MCP config: %w", err)
	}

	f, err := os.CreateTemp("", "dozor-session-mcp-*.json")
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

// loadUserMCPServers reads ~/.mcp.json and returns the mcpServers map.
// Returns an empty map on any error.
func loadUserMCPServers() map[string]any {
	home, err := os.UserHomeDir()
	if err != nil {
		return make(map[string]any)
	}

	data, err := os.ReadFile(home + "/.mcp.json")
	if err != nil {
		return make(map[string]any)
	}

	var cfg struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.MCPServers == nil {
		return make(map[string]any)
	}

	return cfg.MCPServers
}

// toolProgressEntry maps a tool to its emoji and input field key.
type toolProgressEntry struct {
	emoji string
	field string
}

// toolProgressMap defines progress formatting for known tools.
var toolProgressMap = map[string]toolProgressEntry{
	"Read":  {emoji: "📖 Reading ", field: "file_path"},
	"Write": {emoji: "✏️ Writing ", field: "file_path"},
	"Edit":  {emoji: "✏️ Editing ", field: "file_path"},
	"Bash":  {emoji: "🔧 Running: ", field: "command"},
	"Glob":  {emoji: "🔍 Searching files: ", field: "pattern"},
	"Grep":  {emoji: "🔍 Searching: ", field: "pattern"},
	"Task":  {emoji: "🚀 Spawning agent: ", field: "description"},
}

// formatToolProgress returns a human-readable progress notification for a tool invocation.
func formatToolProgress(name string, input map[string]any) string {
	if entry, ok := toolProgressMap[name]; ok {
		if val, ok := input[entry.field].(string); ok {
			val = truncate(val, 100)
			return entry.emoji + val
		}
		return ""
	}

	if strings.HasPrefix(name, "mcp__dozor__") {
		return "🛠️ dozor: " + strings.TrimPrefix(name, "mcp__dozor__")
	}
	if name != "" {
		return "⚙️ " + name
	}
	return ""
}

// truncate limits a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
