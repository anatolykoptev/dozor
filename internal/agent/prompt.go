package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/anatolykoptev/dozor/internal/mcpclient"
	"github.com/anatolykoptev/dozor/internal/skills"
)

// bootstrapFiles are loaded from workspace in this order.
//
// MEMORY.md was previously in this list but was removed deliberately: loading
// the entire file into every session's system prompt encouraged the agent to
// inherit stale or fabricated "recurring patterns" from months-old entries,
// and the file grew unboundedly via blind append (update_memory). MEMORY.md
// is now available on-demand via the `read_memory` tool instead, and the
// canonical incident knowledge base is MemDB via `memdb_search` / `memdb_save`.
var bootstrapFiles = []string{"IDENTITY.md", "AGENTS.md"}

// BuildSystemPrompt constructs the system prompt from workspace files, skills,
// and (optionally) a startup memory snapshot pulled from MemDB via the given
// KBSearcher.
//
// Sections, in order:
//  1. Bootstrap files (IDENTITY.md, AGENTS.md).
//  2. Fallback identity if no bootstrap file was found.
//  3. Skills summary (XML metadata for LLM skill discovery).
//  4. Startup memory snapshot (Phase 6.3) — a single semantic memdb_search
//     call wrapped in <startup_snapshot> tags. Skipped entirely when
//     searcher is nil, and silent on any snapshot error or timeout.
func BuildSystemPrompt(workspacePath string, skillsLoader *skills.Loader, searcher *mcpclient.KBSearcher) string {
	var parts []string

	// 1. Load bootstrap files (IDENTITY.md, AGENTS.md).
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

	// 4. Startup memory snapshot — single semantic query against MemDB so
	// the agent starts each session with the most relevant operational
	// facts already in context. Replaces the raw MEMORY.md load that was
	// removed in Phase 5.7. Silent failure on error or timeout.
	if searcher != nil {
		if snapshot := BuildStartupSnapshot(context.Background(), searcher); snapshot != "" {
			parts = append(parts, snapshot)
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
