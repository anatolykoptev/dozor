package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// toolMemDBDelete is the user-facing tool name exposed to the LLM for
// removing entries from MemDB. Mirrors memdb_search / memdb_save.
const toolMemDBDelete = "memdb_delete"

// RegisterDeleteTool registers the memdb_delete tool when the KB server is
// configured. Safe to call without a server — returns silently.
func RegisterDeleteTool(registry *toolreg.Registry, mgr *ClientManager, cfg KBConfig) {
	cfg.applyDefaults()
	if _, ok := mgr.GetServer(cfg.ServerID); !ok {
		return
	}
	registry.Register(&kbDeleteTool{mgr: mgr, cfg: cfg})
}

// kbDeleteTool is the memdb_delete implementation. Forwards to the native
// delete_memory endpoint on memdb-go via the MCP proxy.
type kbDeleteTool struct {
	mgr *ClientManager
	cfg KBConfig
}

func (t *kbDeleteTool) Name() string { return toolMemDBDelete }

func (t *kbDeleteTool) Description() string {
	return "Delete one or more entries from MemDB by memory ID. Use when a " +
		"previously-saved memory turns out to be wrong, stale, or contaminated " +
		"(e.g. a fabricated narrative, a chat log that leaked into the KB, or " +
		"a resolved incident whose service no longer exists). The `reason` " +
		"parameter is REQUIRED and is logged so future audits can understand " +
		"why a memory was removed — never omit it."
}

func (t *kbDeleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"memory_ids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "List of memory UUIDs to delete. Obtain these from a prior memdb_search result.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Required. Why are these memories being deleted? Examples: 'fabricated narrative from 2026-04-11 TLS-blocked incident', 'chat export auto-saved before Phase 5.7 fix', 'service removed from stack'.",
			},
		},
		"required": []string{"memory_ids", "reason"},
	}
}

func (t *kbDeleteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	rawIDs, _ := args["memory_ids"].([]any)
	if len(rawIDs) == 0 {
		return "", errors.New("memory_ids is required and must be non-empty")
	}
	ids := make([]string, 0, len(rawIDs))
	for _, r := range rawIDs {
		if s, ok := r.(string); ok && s != "" {
			ids = append(ids, s)
		}
	}
	if len(ids) == 0 {
		return "", errors.New("memory_ids contained no valid string values")
	}
	reason, _ := args["reason"].(string)
	if reason == "" {
		return "", errors.New("reason is required — never delete memories silently")
	}

	slog.Info("memdb_delete invoked",
		slog.Int("count", len(ids)),
		slog.String("reason", reason),
		slog.Any("ids", ids))

	// MemDB's native delete endpoint accepts memory_ids + user_id; we pass
	// the cube-scoped user_id so a rogue delete cannot cross tenants.
	result, err := t.mgr.Call(ctx, t.cfg.ServerID, "delete_memory", map[string]any{
		"user_id":    t.cfg.PersonID,
		"memory_ids": ids,
	})
	if err != nil {
		return "", fmt.Errorf("memdb delete failed: %w", err)
	}
	return fmt.Sprintf("Deleted %d memor(ies) from MemDB (cube=%s). Reason: %s\n%s",
		len(ids), t.cfg.CubeID, reason, result), nil
}
