package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/anatolykoptev/dozor/internal/skills"
)

// bootstrapFiles are loaded from workspace in this order.
var bootstrapFiles = []string{"IDENTITY.md", "AGENTS.md", "MEMORY.md"}

// BuildSystemPrompt constructs the system prompt from workspace files and skills.
func BuildSystemPrompt(workspacePath string, skillsLoader *skills.Loader) string {
	var parts []string

	// 1. Load bootstrap files (IDENTITY.md, AGENTS.md, MEMORY.md).
	for _, name := range bootstrapFiles {
		content := loadBootstrapFile(workspacePath, name)
		if content != "" {
			parts = append(parts, content)
		}
	}

	// 2. Fallback: if no IDENTITY.md found, use minimal default.
	if len(parts) == 0 {
		parts = append(parts, fallbackIdentity)
	}

	// 3. Skills summary (XML metadata for LLM to discover available skills).
	if skillsLoader != nil {
		if summary := skillsLoader.BuildSummary(); summary != "" {
			parts = append(parts, summary)
		}
	}

	return strings.Join(parts, "\n\n")
}

func loadBootstrapFile(workspace, name string) string {
	if workspace == "" {
		return ""
	}
	path := filepath.Join(workspace, name)
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("bootstrap file not found", slog.String("file", path))
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	return content
}

const fallbackIdentity = `You are Dozor, an autonomous server monitoring and operations agent.
You manage Docker Compose services, systemd units, deployments, and system resources on a Linux server.

Rules:
- Always diagnose before acting. Use server_triage first when investigating issues.
- After fixing something, verify the fix (check health again).
- Be concise and technical in your responses.
- For destructive actions (cleanup with report=false, prune), confirm the scope first.`
