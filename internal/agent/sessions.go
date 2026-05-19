package agent

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	session "github.com/anatolykoptev/go-kit/session"
)

const (
	defaultCompactionThreshold = 24
	defaultCompactionKeep      = 8
)

// SessionStore wraps session.Store, translating between kitllm.Message and session.Message.
type SessionStore struct {
	store     session.Store
	compactor *session.Compactor
}

// NewSessionStore creates a file-backed session store. If dir is empty, an in-memory store is used.
func NewSessionStore(dir string) *SessionStore {
	var s session.Store
	if dir != "" {
		s = session.NewFileStore(dir, session.Options{})
	} else {
		s = session.NewInMemoryStore(session.Options{})
	}
	return &SessionStore{store: s}
}

// WithCompactor attaches a Compactor using the given LLM summarize function.
func (ss *SessionStore) WithCompactor(summarize session.SummarizeFn) *SessionStore {
	threshold, keepLast := compactionConfig()
	ss.compactor = &session.Compactor{
		Store:        ss.store,
		Summarize:    summarize,
		Threshold:    threshold,
		KeepLast:     keepLast,
		ExtractFacts: false,
	}
	return ss
}

// compactionConfig returns threshold and keep values from env vars.
func compactionConfig() (threshold, keep int) {
	threshold = defaultCompactionThreshold
	keep = defaultCompactionKeep
	if v, err := strconv.Atoi(os.Getenv("DOZOR_SESSION_COMPACTION_THRESHOLD")); err == nil && v > 0 {
		threshold = v
	}
	if v, err := strconv.Atoi(os.Getenv("DOZOR_SESSION_COMPACTION_KEEP")); err == nil && v > 0 {
		keep = v
	}
	return threshold, keep
}

// Add appends a kitllm message to the session.
func (ss *SessionStore) Add(key string, msg kitllm.Message) {
	ss.store.AddMessage(key, toSessionMsg(msg))
}

// Get returns the session history as kitllm messages.
func (ss *SessionStore) Get(key string) []kitllm.Message {
	msgs := ss.store.GetHistory(key)
	if msgs == nil {
		return nil
	}
	out := make([]kitllm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = toKitMessage(m)
	}
	return out
}

// GetSummary returns the compaction summary for a session.
func (ss *SessionStore) GetSummary(key string) string {
	return ss.store.GetSummary(key)
}

// Len returns the message count for a session.
func (ss *SessionStore) Len(key string) int {
	return ss.store.MessageCount(key)
}

// Save persists a session to disk.
func (ss *SessionStore) Save(key string) error {
	return ss.store.Save(key)
}

// Compact runs compaction if a compactor is attached and threshold is reached.
func (ss *SessionStore) Compact(ctx context.Context, key string) {
	if ss.compactor == nil {
		return
	}
	ss.compactor.Compact(ctx, key)
	if err := ss.store.Save(key); err != nil {
		slog.Warn("session save after compaction failed", slog.String("key", key), slog.Any("error", err))
	}
}

// toSessionMsg converts kitllm.Message to session.Message.
// kitllm.Message.Content is `any` (string or []ContentPart); we store
// only the string form since session.Message.Content is string. Multimodal
// messages are not used in dozor today.
func toSessionMsg(m kitllm.Message) session.Message {
	content := ""
	if s, ok := m.Content.(string); ok {
		content = s
	}
	sm := session.Message{
		Role:       m.Role,
		Content:    content,
		ToolCallID: m.ToolCallID,
		ChatTime:   m.ChatTime,
		MessageID:  m.MessageID,
		Name:       m.Name,
	}
	if len(m.ToolCalls) > 0 {
		sm.ToolCalls = make([]session.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			// kitllm.ToolCall.Function is a value type; session.ToolCall.Function is a pointer.
			sm.ToolCalls[i] = session.ToolCall{
				ID: tc.ID,
				Function: &session.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
	}
	return sm
}

// toKitMessage converts session.Message to kitllm.Message.
// session.ToolCall.Function is a pointer; kitllm.ToolCall.Function is a value.
// A nil Function pointer maps to a zero-value kitllm.FunctionCall.
func toKitMessage(m session.Message) kitllm.Message {
	km := kitllm.Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		ChatTime:   m.ChatTime,
		MessageID:  m.MessageID,
		Name:       m.Name,
	}
	if len(m.ToolCalls) > 0 {
		km.ToolCalls = make([]kitllm.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			km.ToolCalls[i] = kitllm.ToolCall{
				ID:   tc.ID,
				Type: "function",
			}
			if tc.Function != nil {
				km.ToolCalls[i].Function = kitllm.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}
			}
		}
	}
	return km
}
