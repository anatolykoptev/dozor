package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// kbSaveMaxAttempts bounds the retry loop for transient MemDB failures.
// Schema validation errors bypass retry entirely (they are not transient).
const kbSaveMaxAttempts = 3

// kbSaveInitialBackoff is the starting delay between retry attempts.
// Doubles per attempt up to kbSaveMaxBackoff, with jitter.
const kbSaveInitialBackoff = 200 * time.Millisecond

// kbSaveMaxBackoff caps the per-attempt delay so a flapping backend
// does not hold the save goroutine for too long.
const kbSaveMaxBackoff = 2 * time.Second

// kbSaveJitterDivisor controls jitter magnitude: jitter = rand(backoff / kbSaveJitterDivisor).
// A divisor of 5 gives ±20% jitter relative to the current backoff window.
const kbSaveJitterDivisor = 5

const (
	// defaultKBUserID is the default user ID for the knowledge base.
	defaultKBUserID = "default"
	// toolMemDBSearch is the user-facing name exposed to the LLM for searching MemDB.
	toolMemDBSearch = "memdb_search"
	// toolMemDBSave is the user-facing name exposed to the LLM for saving to MemDB.
	toolMemDBSave = "memdb_save"
	// defaultSearchTopK is the default number of results returned by memdb_search
	// when the caller does not specify top_k.
	defaultSearchTopK = 5
)

// KBConfig holds configuration for the knowledge base tools.
// Internal type names keep the historical "KB" prefix for grep continuity;
// user-facing tool names are `memdb_search` / `memdb_save` (see toolMemDB* constants).
type KBConfig struct {
	ServerID   string // MCP server ID (default "memdb")
	UserID     string // KB user (legacy; kept for back-compat)
	PersonID   string // Phase 2: person identity sent as MemDB user_id; falls back to UserID
	CubeID     string // KB cube (default "default")
	SearchTool string // MCP tool name for search (default "search_memories")
	SaveTool   string // MCP tool name for save (default "add_memory")
}

// applyDefaults fills in missing KBConfig fields. Extracted so RegisterKBTools
// and NewKBSearcher share a single source of truth.
func (c *KBConfig) applyDefaults() {
	if c.ServerID == "" {
		c.ServerID = "memdb"
	}
	if c.UserID == "" {
		c.UserID = defaultKBUserID
	}
	if c.PersonID == "" {
		c.PersonID = c.UserID // Phase 2 back-compat: default person = legacy UserID
	}
	if c.CubeID == "" {
		c.CubeID = defaultKBUserID
	}
	if c.SearchTool == "" {
		c.SearchTool = "search_memories"
	}
	if c.SaveTool == "" {
		c.SaveTool = "add_memory"
	}
}

// RegisterKBTools adds memdb_search and memdb_save tools that wrap MCP calls
// to the MemDB server. These are simpler than raw mcp_call.
func RegisterKBTools(registry *toolreg.Registry, mgr *ClientManager, cfg KBConfig) {
	cfg.applyDefaults()

	// Only register if the KB server is configured.
	if _, ok := mgr.GetServer(cfg.ServerID); !ok {
		return
	}

	registry.Register(&kbSearchTool{mgr: mgr, cfg: cfg, cache: newSearchCache()})
	registry.Register(&kbSaveTool{mgr: mgr, cfg: cfg})
}

// --- memdb_search ---

type kbSearchTool struct {
	mgr   *ClientManager
	cfg   KBConfig
	cache *searchCache
}

func (t *kbSearchTool) Name() string { return toolMemDBSearch }
func (t *kbSearchTool) Description() string {
	return "Search the shared MemDB knowledge base for past incidents, solutions, and operational patterns. " +
		"Use BEFORE fixing any non-trivial issue to find proven solutions from previous sessions and sibling agents. " +
		"Backed by `search_memories` on the memdb MCP server; results are semantically ranked and deduped."
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
		return "", errors.New("query is required")
	}

	topK := defaultSearchTopK
	if v, ok := args["top_k"].(float64); ok && v > 0 {
		topK = int(v)
	}

	// Cache key includes everything that affects the underlying MCP call
	// so two distinct queries cannot share an entry.
	ck := cacheKey(query, t.cfg.PersonID, t.cfg.CubeID, topK)
	if t.cache != nil {
		if cached, hit := t.cache.get(ck); hit {
			return cached, nil
		}
	}

	result, err := t.mgr.Call(ctx, t.cfg.ServerID, t.cfg.SearchTool, map[string]any{
		"query":      query,
		"user_id":    t.cfg.PersonID,
		"cube_ids":   []string{t.cfg.CubeID},
		"top_k":      topK,
		"relativity": 0.82,
		"dedup":      "mmr",
	})
	if err != nil {
		return "", fmt.Errorf("memdb search failed: %w", err)
	}

	formatted := formatSearchResult(result)
	if t.cache != nil {
		t.cache.set(ck, formatted)
	}
	return formatted, nil
}

// --- memdb_save ---

type kbSaveTool struct {
	mgr *ClientManager
	cfg KBConfig
}

func (t *kbSaveTool) Name() string { return toolMemDBSave }
func (t *kbSaveTool) Description() string {
	return "Save an incident resolution, operational fact, or anti-pattern to the shared MemDB knowledge base. " +
		"Use AFTER resolving a non-trivial issue. The entry must cite concrete tool output (commands + their results) — " +
		"do not save vague narratives. Format: `Incident: <service> <symptom>\\nEvidence: <quoted tool output>\\n" +
		"Root cause: <why>\\nFix: <exact commands>\\nPrevention: <how to avoid recurrence>`. " +
		"Entries are shared across dozor, vaelor, and claude-code sessions."
}
func (t *kbSaveTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "Knowledge to save. Must include Incident, Evidence (quoted tool output), Root cause, Fix, Prevention.",
			},
		},
		"required": []string{"content"},
	}
}

func (t *kbSaveTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	// Validate unconditionally — the validator handles empty content and
	// returns an ErrInvalidSavePayload-wrapped error with a useful remediation
	// message. Skipping the pre-check keeps both save paths (this tool and
	// KBSearcher.Save) consistent so errors.Is(err, ErrInvalidSavePayload)
	// succeeds from either entry point.
	if err := ValidateSavePayload(content); err != nil {
		return "", err
	}

	result, err := t.mgr.Call(ctx, t.cfg.ServerID, t.cfg.SaveTool, map[string]any{
		"user_id":        t.cfg.PersonID,
		"mem_cube_id":    t.cfg.CubeID,
		"memory_content": content,
	})
	if err != nil {
		return "", fmt.Errorf("memdb save failed: %w", err)
	}

	return "Knowledge saved to MemDB.\n" + result, nil
}

// KBSearcher provides programmatic (non-tool) access to the MemDB knowledge base.
type KBSearcher struct {
	mgr *ClientManager
	cfg KBConfig
	cb  *engine.CircuitBreaker
}

// NewKBSearcher creates a KBSearcher if the KB server is configured. Returns nil otherwise.
// If cb is non-nil, it is used to protect against cascading failures.
func NewKBSearcher(mgr *ClientManager, cfg KBConfig, cb *engine.CircuitBreaker) *KBSearcher {
	cfg.applyDefaults()
	if _, ok := mgr.GetServer(cfg.ServerID); !ok {
		return nil
	}
	return &KBSearcher{mgr: mgr, cfg: cfg, cb: cb}
}

// ErrKBUnavailable is returned by Search/Save when the circuit breaker is open.
// Callers can distinguish this from a real backend error and decide whether to
// skip persistence silently (startup snapshot) or surface it (user-initiated save).
var ErrKBUnavailable = errors.New("memdb temporarily unavailable (circuit breaker open)")

// Search queries the knowledge base and returns formatted results.
func (s *KBSearcher) Search(ctx context.Context, query string, topK int) (string, error) {
	if topK <= 0 {
		topK = 3
	}
	if s.cb != nil && !s.cb.Allow() {
		return "", ErrKBUnavailable
	}
	result, err := s.mgr.Call(ctx, s.cfg.ServerID, s.cfg.SearchTool, map[string]any{
		"query":      query,
		"user_id":    s.cfg.PersonID,
		"cube_ids":   []string{s.cfg.CubeID},
		"top_k":      topK,
		"relativity": 0.82,
		"dedup":      "mmr",
	})
	if err != nil {
		if s.cb != nil {
			s.cb.RecordFailure()
		}
		return "", fmt.Errorf("memdb search failed: %w", err)
	}
	if s.cb != nil {
		s.cb.RecordSuccess()
	}
	return formatSearchResult(result), nil
}

// Save stores content in the knowledge base.
//
// Pipeline:
//  1. Schema validation (not retried — caller-side bug).
//  2. Circuit breaker gate (returns ErrKBUnavailable if open).
//  3. Up to kbSaveMaxAttempts MCP calls with exponential backoff + jitter.
//     Transient failures bump the breaker failure counter only on the last
//     attempt so a single backend hiccup does not trip the breaker.
//
// Returns ErrKBUnavailable if the breaker is open, ErrInvalidSavePayload
// (wrapped) if the content is schema-rejected, or the last backend error
// after all retries are exhausted. Context cancellation is honored between
// attempts — if ctx is cancelled during a backoff sleep, Save returns the
// cancellation error and records a failure on the breaker.
func (s *KBSearcher) Save(ctx context.Context, content string) error {
	if err := ValidateSavePayload(content); err != nil {
		// Schema rejections do not open the circuit breaker — they are the
		// caller's fault, not the backend's.
		return err
	}
	if s.cb != nil && !s.cb.Allow() {
		return ErrKBUnavailable
	}

	var lastErr error
	backoff := kbSaveInitialBackoff
	for attempt := 1; attempt <= kbSaveMaxAttempts; attempt++ {
		_, err := s.mgr.Call(ctx, s.cfg.ServerID, s.cfg.SaveTool, map[string]any{
			"user_id":        s.cfg.PersonID,
			"mem_cube_id":    s.cfg.CubeID,
			"memory_content": content,
		})
		if err == nil {
			if s.cb != nil {
				s.cb.RecordSuccess()
			}
			return nil
		}
		lastErr = err
		if attempt == kbSaveMaxAttempts {
			break
		}
		// Exponential backoff with ±20% jitter. math/rand is intentional —
		// jitter does not require cryptographic randomness.
		jitter := time.Duration(rand.Int63n(int64(backoff) / kbSaveJitterDivisor)) //nolint:gosec
		select {
		case <-ctx.Done():
			if s.cb != nil {
				s.cb.RecordFailure()
			}
			return fmt.Errorf("memdb save cancelled after %d attempts: %w", attempt, ctx.Err())
		case <-time.After(backoff + jitter):
		}
		backoff *= 2
		if backoff > kbSaveMaxBackoff {
			backoff = kbSaveMaxBackoff
		}
	}

	if s.cb != nil {
		s.cb.RecordFailure()
	}
	return fmt.Errorf("memdb save failed after %d attempts: %w", kbSaveMaxAttempts, lastErr)
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
