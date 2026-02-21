package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// RegisterMemoryTools adds read_memory and update_memory tools.
func RegisterMemoryTools(registry *toolreg.Registry, workspacePath string) {
	registry.Register(&readMemoryTool{workspace: workspacePath})
	registry.Register(&updateMemoryTool{workspace: workspacePath})
}

// --- read_memory ---

type readMemoryTool struct{ workspace string }

func (t *readMemoryTool) Name() string        { return "read_memory" }
func (t *readMemoryTool) Description() string  { return "Read a bootstrap file (IDENTITY.md, AGENTS.md, or MEMORY.md) from the workspace" }
func (t *readMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "File to read: IDENTITY.md, AGENTS.md, or MEMORY.md",
				"enum":        []string{"IDENTITY.md", "AGENTS.md", "MEMORY.md"},
			},
		},
		"required": []string{"file"},
	}
}

func (t *readMemoryTool) Execute(_ context.Context, args map[string]any) (string, error) {
	file, _ := args["file"].(string)
	if !isAllowedFile(file) {
		return "", fmt.Errorf("not allowed: only IDENTITY.md, AGENTS.md, MEMORY.md")
	}
	data, err := os.ReadFile(filepath.Join(t.workspace, file))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	return string(data), nil
}

// --- update_memory ---

type updateMemoryTool struct{ workspace string }

func (t *updateMemoryTool) Name() string        { return "update_memory" }
func (t *updateMemoryTool) Description() string {
	return "Add a new entry to MEMORY.md — use this to record learned patterns, resolved incidents, and operational notes for future reference"
}
func (t *updateMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "Short title for the memory entry (e.g. 'api-service OOM fix')",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The memory content. Include: symptoms, root cause, fix applied, and any notes for next time.",
			},
		},
		"required": []string{"title", "content"},
	}
}

func (t *updateMemoryTool) Execute(_ context.Context, args map[string]any) (string, error) {
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	if title == "" || content == "" {
		return "", fmt.Errorf("both title and content are required")
	}

	memoryPath := filepath.Join(t.workspace, "MEMORY.md")

	// Format the entry.
	timestamp := time.Now().Format("2006-01-02 15:04")
	entry := fmt.Sprintf("\n### %s\n_Recorded: %s_\n\n%s\n", title, timestamp, strings.TrimSpace(content))

	// Append to MEMORY.md.
	f, err := os.OpenFile(memoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return "", fmt.Errorf("open MEMORY.md: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return "", fmt.Errorf("write to MEMORY.md: %w", err)
	}

	return fmt.Sprintf("Memory saved: %q", title), nil
}

func isAllowedFile(name string) bool {
	switch name {
	case "IDENTITY.md", "AGENTS.md", "MEMORY.md":
		return true
	}
	return false
}
