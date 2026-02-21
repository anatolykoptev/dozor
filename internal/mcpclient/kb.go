package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// KBConfig holds configuration for the knowledge base tools.
type KBConfig struct {
	ServerID   string // MCP server ID (default "memdb")
	UserID     string // KB user (default "default")
	CubeID     string // KB cube (default "default")
	SearchTool string // MCP tool name for search (default "search_memories")
	SaveTool   string // MCP tool name for save (default "add_memory")
}

// RegisterKBTools adds kb_search and kb_save tools that wrap MCP calls
// to an external knowledge base server. These are simpler than raw mcp_call.
func RegisterKBTools(registry *toolreg.Registry, mgr *ClientManager, cfg KBConfig) {
	if cfg.ServerID == "" {
		cfg.ServerID = "memdb"
	}
	if cfg.UserID == "" {
		cfg.UserID = "default"
	}
	if cfg.CubeID == "" {
		cfg.CubeID = "default"
	}
	if cfg.SearchTool == "" {
		cfg.SearchTool = "search_memories"
	}
	if cfg.SaveTool == "" {
		cfg.SaveTool = "add_memory"
	}

	// Only register if the KB server is configured.
	if _, ok := mgr.GetServer(cfg.ServerID); !ok {
		return
	}

	registry.Register(&kbSearchTool{mgr: mgr, cfg: cfg})
	registry.Register(&kbSaveTool{mgr: mgr, cfg: cfg})
}

// --- kb_search ---

type kbSearchTool struct {
	mgr *ClientManager
	cfg KBConfig
}

func (t *kbSearchTool) Name() string { return "kb_search" }
func (t *kbSearchTool) Description() string {
	return "Search the knowledge base for past incidents, solutions, and operational patterns. Use BEFORE fixing issues to find proven solutions."
}
func (t *kbSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query (e.g. 'api-service 401 unauthorized', 'postgres connection refused', 'disk usage cleanup')",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return (default 5)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *kbSearchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	topK := 5
	if v, ok := args["top_k"].(float64); ok && v > 0 {
		topK = int(v)
	}

	result, err := t.mgr.Call(ctx, t.cfg.ServerID, t.cfg.SearchTool, map[string]any{
		"query":      query,
		"user_id":    t.cfg.UserID,
		"cube_ids":   []string{t.cfg.CubeID},
		"top_k":      topK,
		"relativity": 0.82,
		"dedup":      "mmr",
	})
	if err != nil {
		return "", fmt.Errorf("kb search failed: %w", err)
	}

	return formatSearchResult(result), nil
}

// --- kb_save ---

type kbSaveTool struct {
	mgr *ClientManager
	cfg KBConfig
}

func (t *kbSaveTool) Name() string { return "kb_save" }
func (t *kbSaveTool) Description() string {
	return "Save an incident resolution, operational pattern, or knowledge to the shared knowledge base. Use AFTER resolving non-trivial issues."
}
func (t *kbSaveTool) Parameters() map[string]any {
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

func (t *kbSaveTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}

	result, err := t.mgr.Call(ctx, t.cfg.ServerID, t.cfg.SaveTool, map[string]any{
		"user_id":        t.cfg.UserID,
		"mem_cube_id":    t.cfg.CubeID,
		"memory_content": content,
	})
	if err != nil {
		return "", fmt.Errorf("kb save failed: %w", err)
	}

	return "Knowledge saved to knowledge base.\n" + result, nil
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
		return "No relevant knowledge found."
	}

	return fmt.Sprintf("Found %d relevant memories:\n\n%s", len(parts), strings.Join(parts, "\n"))
}

// cleanMemory strips the "user: [timestamp]:" prefix that some KB backends add.
func cleanMemory(s string) string {
	// Format: "user: [2026-02-21T00:57:54]: actual content"
	if idx := strings.Index(s, "]: "); idx > 0 && idx < 40 {
		return s[idx+3:]
	}
	return s
}
