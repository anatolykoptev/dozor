package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// MemDBConfig holds configuration for dedicated MemDB tools.
type MemDBConfig struct {
	ServerID string // MCP server ID (default "memdb")
	UserID   string // MemDB user (default "devops")
	CubeID   string // MemDB cube (default "devops")
}

// RegisterMemDBTools adds memdb_search and memdb_save tools that wrap MCP calls
// to the MemDB devops knowledge base. These are simpler than raw mcp_call.
func RegisterMemDBTools(registry *toolreg.Registry, mgr *ClientManager, cfg MemDBConfig) {
	if cfg.ServerID == "" {
		cfg.ServerID = "memdb"
	}
	if cfg.UserID == "" {
		cfg.UserID = "devops"
	}
	if cfg.CubeID == "" {
		cfg.CubeID = "devops"
	}

	// Only register if the memdb server is configured.
	if _, ok := mgr.GetServer(cfg.ServerID); !ok {
		return
	}

	registry.Register(&memdbSearchTool{mgr: mgr, cfg: cfg})
	registry.Register(&memdbSaveTool{mgr: mgr, cfg: cfg})
}

// --- memdb_search ---

type memdbSearchTool struct {
	mgr *ClientManager
	cfg MemDBConfig
}

func (t *memdbSearchTool) Name() string { return "memdb_search" }
func (t *memdbSearchTool) Description() string {
	return "Search the DevOps knowledge base for past incidents, solutions, and operational patterns. Use BEFORE fixing issues to find proven solutions."
}
func (t *memdbSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query (e.g. 'memdb-api 401 unauthorized', 'postgres connection refused', 'disk usage cleanup')",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return (default 5)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *memdbSearchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	topK := 5
	if v, ok := args["top_k"].(float64); ok && v > 0 {
		topK = int(v)
	}

	result, err := t.mgr.Call(ctx, t.cfg.ServerID, "search_memories", map[string]any{
		"query":      query,
		"user_id":    t.cfg.UserID,
		"cube_ids":   []string{t.cfg.CubeID},
		"top_k":      topK,
		"relativity": 0.82,
		"dedup":      "mmr",
	})
	if err != nil {
		return "", fmt.Errorf("memdb search failed: %w", err)
	}

	return formatSearchResult(result), nil
}

// --- memdb_save ---

type memdbSaveTool struct {
	mgr *ClientManager
	cfg MemDBConfig
}

func (t *memdbSaveTool) Name() string { return "memdb_save" }
func (t *memdbSaveTool) Description() string {
	return "Save an incident resolution, operational pattern, or DevOps knowledge to the shared knowledge base. Use AFTER resolving non-trivial issues."
}
func (t *memdbSaveTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "Knowledge to save. For incidents use format: Incident: [service] [description]\\nSymptom: [what was observed]\\nRoot cause: [why it happened]\\nFix: [exact commands/actions]\\nPrevention: [how to prevent recurrence]",
			},
		},
		"required": []string{"content"},
	}
}

func (t *memdbSaveTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}

	result, err := t.mgr.Call(ctx, t.cfg.ServerID, "add_memory", map[string]any{
		"user_id":     t.cfg.UserID,
		"mem_cube_id": t.cfg.CubeID,
		"memory_content": content,
	})
	if err != nil {
		return "", fmt.Errorf("memdb save failed: %w", err)
	}

	return "Knowledge saved to DevOps knowledge base.\n" + result, nil
}

// formatSearchResult extracts readable memories from the raw MCP JSON response.
func formatSearchResult(raw string) string {
	// Parse the nested JSON response to extract memories.
	var resp struct {
		Result struct {
			Data struct {
				TextMem []struct {
					Memories []struct {
						Memory string `json:"memory"`
					} `json:"memories"`
				} `json:"text_mem"`
				ActMem []struct {
					Memory string `json:"memory"`
				} `json:"act_mem"`
				SkillMem []struct {
					Memories []struct {
						Memory string `json:"memory"`
					} `json:"memories"`
				} `json:"skill_mem"`
			} `json:"data"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		// Fallback: return raw if can't parse.
		return raw
	}

	var parts []string

	// Text memories (long-term facts).
	for _, cube := range resp.Result.Data.TextMem {
		for _, m := range cube.Memories {
			if m.Memory != "" {
				parts = append(parts, "- "+cleanMemory(m.Memory))
			}
		}
	}

	// Active/working memories.
	for _, m := range resp.Result.Data.ActMem {
		if m.Memory != "" {
			parts = append(parts, "- "+cleanMemory(m.Memory))
		}
	}

	// Skill memories.
	for _, cube := range resp.Result.Data.SkillMem {
		for _, m := range cube.Memories {
			if m.Memory != "" {
				parts = append(parts, "- [skill] "+cleanMemory(m.Memory))
			}
		}
	}

	if len(parts) == 0 {
		return "No relevant knowledge found in the DevOps knowledge base."
	}

	return fmt.Sprintf("Found %d relevant memories:\n\n%s", len(parts), strings.Join(parts, "\n"))
}

// cleanMemory strips the "user: [timestamp]:" prefix that MemDB adds.
func cleanMemory(s string) string {
	// Format: "user: [2026-02-21T00:57:54]: actual content"
	if idx := strings.Index(s, "]: "); idx > 0 && idx < 40 {
		return s[idx+3:]
	}
	return s
}
