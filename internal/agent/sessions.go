package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"

	session "github.com/anatolykoptev/go-session"
	"github.com/anatolykoptev/dozor/internal/provider"
)

const (
	defaultCompactionThreshold = 24
	defaultCompactionKeep      = 8
)

// SessionStore wraps session.Store, translating between provider.Message and session.Message.
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

// Add appends a provider message to the session.
func (ss *SessionStore) Add(key string, msg provider.Message) {
	ss.store.AddMessage(key, toSessionMsg(msg))
}

// Get returns the session history as provider messages.
func (ss *SessionStore) Get(key string) []provider.Message {
	msgs := ss.store.GetHistory(key)
	if msgs == nil {
		return nil
	}
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		out[i] = toProviderMsg(m)
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

// toSessionMsg converts provider.Message to session.Message.
func toSessionMsg(m provider.Message) session.Message {
	sm := session.Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		sm.ToolCalls = make([]session.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			sm.ToolCalls[i] = session.ToolCall{
				ID:   tc.ID,
				Name: tc.Name,
			}
			if tc.Function != nil {
				sm.ToolCalls[i].Function = &session.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}
			}
			if tc.Args != nil {
				if b, err := json.Marshal(tc.Args); err == nil {
					sm.ToolCalls[i].Args = string(b)
				}
			}
		}
	}
	return sm
}

// toProviderMsg converts session.Message to provider.Message.
func toProviderMsg(m session.Message) provider.Message {
	pm := provider.Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		pm.ToolCalls = make([]provider.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			pm.ToolCalls[i] = provider.ToolCall{
				ID:   tc.ID,
				Name: tc.Name,
			}
			if tc.Function != nil {
				pm.ToolCalls[i].Function = &provider.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}
			}
			if tc.Args != "" {
				var args map[string]any
				if err := json.Unmarshal([]byte(tc.Args), &args); err == nil {
					pm.ToolCalls[i].Args = args
				}
			}
		}
	}
	return pm
}
