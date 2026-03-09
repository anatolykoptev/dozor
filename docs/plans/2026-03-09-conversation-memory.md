# Dozor Conversation Memory Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Give Dozor contextual conversation memory (session history) and long-term memory (MemDB injection) so it sees previous messages in dialogue.

**Architecture:** Add a `SessionStore` to the agent package that persists per-chatID message history to JSON files in `~/.dozor/sessions/`. Modify `Loop.Process()` to accept a session key and inject history + summary into the LLM messages. Add MemDB context injection in the gateway message handler before calling `Process()`. Add compaction (LLM-summarize old messages) when history exceeds threshold.

**Tech Stack:** Go stdlib, existing `provider.Provider` interface, existing `mcpclient.KBSearcher`, JSON file storage.

**Root cause:** `Loop.Process()` (line 43-47 of `internal/agent/loop.go`) creates `messages` from scratch every call — only system prompt + current user message. No history is carried over.

---

## Task 1: SessionStore — Core Data Structure + File Persistence

**Files:**
- Create: `internal/agent/session_store.go`
- Create: `internal/agent/session_store_test.go`

**Step 1: Write the failing test**

```go
// internal/agent/session_store_test.go
package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anatolykoptev/dozor/internal/provider"
)

func TestSessionStore_AddAndGet(t *testing.T) {
	store := NewSessionStore("")
	store.Add("chat1", provider.Message{Role: "user", Content: "hello"})
	store.Add("chat1", provider.Message{Role: "assistant", Content: "hi"})

	got := store.Get("chat1")
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Content != "hello" {
		t.Errorf("got[0].Content = %q, want %q", got[0].Content, "hello")
	}
	if got[1].Content != "hi" {
		t.Errorf("got[1].Content = %q, want %q", got[1].Content, "hi")
	}
}

func TestSessionStore_GetEmpty(t *testing.T) {
	store := NewSessionStore("")
	got := store.Get("nonexistent")
	if len(got) != 0 {
		t.Fatalf("got %d messages, want 0", len(got))
	}
}

func TestSessionStore_IsolatedSessions(t *testing.T) {
	store := NewSessionStore("")
	store.Add("chat1", provider.Message{Role: "user", Content: "a"})
	store.Add("chat2", provider.Message{Role: "user", Content: "b"})

	if len(store.Get("chat1")) != 1 {
		t.Error("chat1 should have 1 message")
	}
	if len(store.Get("chat2")) != 1 {
		t.Error("chat2 should have 1 message")
	}
}

func TestSessionStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	// Write to store, save.
	store1 := NewSessionStore(dir)
	store1.Add("chat1", provider.Message{Role: "user", Content: "persist me"})
	store1.Add("chat1", provider.Message{Role: "assistant", Content: "ok"})
	if err := store1.Save("chat1"); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// New store loads from disk.
	store2 := NewSessionStore(dir)
	got := store2.Get("chat1")
	if len(got) != 2 {
		t.Fatalf("got %d messages after reload, want 2", len(got))
	}
	if got[0].Content != "persist me" {
		t.Errorf("got[0].Content = %q, want %q", got[0].Content, "persist me")
	}
}

func TestSessionStore_Clear(t *testing.T) {
	store := NewSessionStore("")
	store.Add("chat1", provider.Message{Role: "user", Content: "a"})
	store.Add("chat1", provider.Message{Role: "user", Content: "b"})
	store.Clear("chat1")

	if len(store.Get("chat1")) != 0 {
		t.Error("expected empty after Clear")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/src/dozor && go test ./internal/agent/ -run TestSessionStore -v`
Expected: FAIL — `NewSessionStore` undefined

**Step 3: Write minimal implementation**

```go
// internal/agent/session_store.go
package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anatolykoptev/dozor/internal/provider"
)

// maxHistoryLen is the default maximum messages kept per session before compaction is needed.
const maxHistoryLen = 40

// sessionData holds the serializable session state.
type sessionData struct {
	Messages []provider.Message `json:"messages"`
	Summary  string             `json:"summary,omitempty"`
}

// SessionStore manages per-chat conversation history with optional file persistence.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*sessionData
	dir      string // empty = no persistence
}

// NewSessionStore creates a session store. If dir is non-empty, existing sessions are loaded.
func NewSessionStore(dir string) *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*sessionData),
		dir:      dir,
	}
	if dir != "" {
		_ = os.MkdirAll(dir, 0700)
		s.loadAll()
	}
	return s
}

// Add appends a message to the session's history.
func (s *SessionStore) Add(key string, msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd := s.getOrCreate(key)
	sd.Messages = append(sd.Messages, msg)
}

// Get returns a copy of the session's message history.
func (s *SessionStore) Get(key string) []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sd, ok := s.sessions[key]
	if !ok {
		return nil
	}
	out := make([]provider.Message, len(sd.Messages))
	copy(out, sd.Messages)
	return out
}

// GetSummary returns the compacted summary for a session.
func (s *SessionStore) GetSummary(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sd, ok := s.sessions[key]; ok {
		return sd.Summary
	}
	return ""
}

// SetSummary stores a compacted summary for a session.
func (s *SessionStore) SetSummary(key, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd := s.getOrCreate(key)
	sd.Summary = summary
}

// Len returns the number of messages in a session.
func (s *SessionStore) Len(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sd, ok := s.sessions[key]; ok {
		return len(sd.Messages)
	}
	return 0
}

// Truncate removes the oldest messages, keeping only the last keepLast.
// Returns the removed messages (for summarization).
func (s *SessionStore) Truncate(key string, keepLast int) []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd, ok := s.sessions[key]
	if !ok || len(sd.Messages) <= keepLast {
		return nil
	}
	cutoff := len(sd.Messages) - keepLast
	removed := make([]provider.Message, cutoff)
	copy(removed, sd.Messages[:cutoff])
	sd.Messages = sd.Messages[cutoff:]
	return removed
}

// Clear removes all messages and summary for a session.
func (s *SessionStore) Clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// Save persists a single session to disk. No-op if dir is empty.
func (s *SessionStore) Save(key string) error {
	if s.dir == "" {
		return nil
	}
	s.mu.RLock()
	sd, ok := s.sessions[key]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	data, err := json.Marshal(sd)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	path := s.filePath(key)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *SessionStore) getOrCreate(key string) *sessionData {
	sd, ok := s.sessions[key]
	if !ok {
		sd = &sessionData{}
		s.sessions[key] = sd
	}
	return sd
}

func (s *SessionStore) filePath(key string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(key)
	return filepath.Join(s.dir, safe+".json")
}

func (s *SessionStore) loadAll() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sd sessionData
		if err := json.Unmarshal(data, &sd); err != nil {
			slog.Debug("skip corrupt session file", slog.String("file", e.Name()))
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".json")
		s.sessions[key] = &sd
	}
}
```

**Step 4: Run test to verify it passes**

Run: `cd ~/src/dozor && go test ./internal/agent/ -run TestSessionStore -v`
Expected: PASS (all 5 tests)

**Step 5: Commit**

```bash
cd ~/src/dozor
git add internal/agent/session_store.go internal/agent/session_store_test.go
git commit -m "feat(agent): add SessionStore for per-chat conversation history"
```

---

## Task 2: Wire SessionStore into Loop.Process()

**Files:**
- Modify: `internal/agent/loop.go` (lines 25-47 — add session store, change Process signature)
- Modify: `internal/agent/loop_test.go` (update all callers of Process)

**Step 1: Write the failing test for history injection**

Add to `internal/agent/loop_test.go`:

```go
func TestProcess_HistoryInjected(t *testing.T) {
	// Pre-populate session history.
	store := NewSessionStore("")
	store.Add("chat1", provider.Message{Role: "user", Content: "my name is Alice"})
	store.Add("chat1", provider.Message{Role: "assistant", Content: "nice to meet you, Alice"})

	// Capture what the provider receives.
	var capturedMsgs []provider.Message
	p := &capturingProvider{
		onChat: func(msgs []provider.Message) {
			capturedMsgs = msgs
		},
		inner: &mockProvider{responses: []mockResponse{textResp("hello Alice")}},
	}

	l := newTestLoop(p, toolreg.NewRegistry(), 10)
	l.sessions = store

	_, err := l.Process(context.Background(), "chat1", "what is my name?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: system + 2 history + 1 current = 4 messages
	if len(capturedMsgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(capturedMsgs))
	}
	if capturedMsgs[0].Role != "system" {
		t.Error("first message should be system")
	}
	if capturedMsgs[1].Content != "my name is Alice" {
		t.Errorf("history[0] = %q, want %q", capturedMsgs[1].Content, "my name is Alice")
	}
	if capturedMsgs[3].Content != "what is my name?" {
		t.Errorf("current = %q, want %q", capturedMsgs[3].Content, "what is my name?")
	}

	// Check that history was persisted (user + assistant added).
	history := store.Get("chat1")
	if len(history) != 4 {
		t.Errorf("history should have 4 messages (2 old + 2 new), got %d", len(history))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/src/dozor && go test ./internal/agent/ -run TestProcess_HistoryInjected -v`
Expected: FAIL — `l.sessions` field doesn't exist, wrong number of args to Process

**Step 3: Modify Loop struct and Process method**

Changes to `internal/agent/loop.go`:

1. Add `sessions *SessionStore` field to `Loop` struct
2. Add `WithSessions(s *SessionStore) *Loop` method
3. Change `Process(ctx, message)` → `Process(ctx, sessionKey, message)`
4. In Process: inject history from sessions, persist user+assistant after

```go
// In Loop struct, add:
sessions *SessionStore

// Add method:
func (l *Loop) WithSessions(s *SessionStore) *Loop {
	l.sessions = s
	return l
}

// In Process — replace lines 43-47 with:
func (l *Loop) Process(ctx context.Context, sessionKey, message string) (string, error) {
	messages := []provider.Message{
		{Role: "system", Content: l.systemPrompt},
	}

	// Inject conversation history.
	if l.sessions != nil && sessionKey != "" {
		if summary := l.sessions.GetSummary(sessionKey); summary != "" {
			messages = append(messages, provider.Message{
				Role:    "user",
				Content: "[Previous conversation summary]\n" + summary,
			})
			messages = append(messages, provider.Message{
				Role:    "assistant",
				Content: "Understood, I have the context from our previous conversation.",
			})
		}
		messages = append(messages, l.sessions.Get(sessionKey)...)
	}

	messages = append(messages, provider.Message{Role: "user", Content: message})
	// ... rest of the loop unchanged ...
```

After the loop produces a response (line 74 `return content, nil`), add persistence:

```go
	// Persist to session history.
	if l.sessions != nil && sessionKey != "" {
		l.sessions.Add(sessionKey, provider.Message{Role: "user", Content: message})
		l.sessions.Add(sessionKey, provider.Message{Role: "assistant", Content: content})
		_ = l.sessions.Save(sessionKey)
	}
	return content, nil
```

**Step 4: Update all existing tests**

All existing `Process(ctx, "message")` calls become `Process(ctx, "", "message")` — empty sessionKey means no history.

**Step 5: Run all tests**

Run: `cd ~/src/dozor && go test ./internal/agent/ -v`
Expected: ALL PASS

**Step 6: Commit**

```bash
cd ~/src/dozor
git add internal/agent/loop.go internal/agent/loop_test.go
git commit -m "feat(agent): inject session history into LLM context in Process()"
```

---

## Task 3: Update All Callers of Process()

**Files:**
- Modify: `cmd/dozor/gateway_messages.go` (line 93 — add chatID)
- Modify: `cmd/dozor/watch.go` (line 103 — use "watch" session key)
- Modify: `cmd/dozor/setup.go` (create SessionStore, pass to Loop)
- Modify: `internal/a2a/executor.go` (line 47 — need session key)

**Step 1: Update setup.go — create SessionStore and wire it**

In `buildAgentStack()`:

```go
sessionsDir := workspacePath + "/sessions"
sessions := agent.NewSessionStore(sessionsDir)
loop := agent.NewLoop(llm, registry, llm.MaxIterations(), workspacePath, skillsLoader)
loop.WithSessions(sessions)
```

Add `sessions *agent.SessionStore` field to `agentStack`.

**Step 2: Update gateway_messages.go — pass chatID**

Line 93: `response, err := deps.stack.loop.Process(ctx, msg.Text)` →
`response, err := deps.stack.loop.Process(ctx, msg.ChatID, msg.Text)`

Same for `autoEscalateToClaudeCode`: use `"escalation"` as session key.

**Step 3: Update watch.go — use "watch" session key**

Line 103: `response, err := loop.Process(ctx, prompt)` →
`response, err := loop.Process(ctx, "watch", prompt)`

Also in `buildSmartWatchLoop()` — create SessionStore there too (or pass empty key).

**Step 4: Update a2a/executor.go — extract session key from request**

Line 47: `response, err := e.proc.Process(ctx, content)` →
Need to update `MessageProcessor` interface to include session key.

Change `MessageProcessor`:
```go
type MessageProcessor interface {
	Process(ctx context.Context, sessionKey, message string) (string, error)
}
```

In `Execute()`, derive sessionKey from `reqCtx.TaskID` or message ID:
```go
sessionKey := "a2a:" + reqCtx.TaskID
response, err := e.proc.Process(ctx, sessionKey, content)
```

**Step 5: Run full test suite**

Run: `cd ~/src/dozor && go test ./... -v`
Expected: ALL PASS

**Step 6: Commit**

```bash
cd ~/src/dozor
git add cmd/dozor/setup.go cmd/dozor/gateway_messages.go cmd/dozor/watch.go internal/a2a/executor.go
git commit -m "feat: wire SessionStore through all Process() callers"
```

---

## Task 4: Session Compaction (LLM Summarization)

**Files:**
- Create: `internal/agent/compaction.go`
- Create: `internal/agent/compaction_test.go`

**Step 1: Write the failing test**

```go
// internal/agent/compaction_test.go
package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

func TestCompactSession_UnderThreshold(t *testing.T) {
	store := NewSessionStore("")
	for i := 0; i < 5; i++ {
		store.Add("chat1", provider.Message{Role: "user", Content: "msg"})
	}

	p := &mockProvider{}
	l := newTestLoop(p, toolreg.NewRegistry(), 10)
	l.sessions = store

	l.CompactSession(context.Background(), "chat1")

	// Under threshold — no compaction, no LLM call.
	if p.callCount != 0 {
		t.Errorf("provider should not be called, got %d", p.callCount)
	}
}

func TestCompactSession_OverThreshold(t *testing.T) {
	store := NewSessionStore("")
	for i := 0; i < compactionThreshold+5; i++ {
		store.Add("chat1", provider.Message{
			Role:    "user",
			Content: "message " + strings.Repeat("x", 10),
		})
	}

	p := &mockProvider{responses: []mockResponse{
		textResp("Summary: user sent many test messages"),
	}}
	l := newTestLoop(p, toolreg.NewRegistry(), 10)
	l.sessions = store

	l.CompactSession(context.Background(), "chat1")

	// LLM should be called once to summarize.
	if p.callCount != 1 {
		t.Errorf("provider should be called once for summarization, got %d", p.callCount)
	}

	// Summary should be set.
	summary := store.GetSummary("chat1")
	if summary == "" {
		t.Error("summary should be set after compaction")
	}

	// History should be truncated to compactionKeep messages.
	if store.Len("chat1") != compactionKeep {
		t.Errorf("history should have %d messages after compaction, got %d", compactionKeep, store.Len("chat1"))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/src/dozor && go test ./internal/agent/ -run TestCompactSession -v`
Expected: FAIL — `CompactSession` undefined

**Step 3: Implement compaction**

```go
// internal/agent/compaction.go
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/dozor/internal/provider"
)

const (
	// compactionThreshold is the number of messages that triggers compaction.
	compactionThreshold = 24
	// compactionKeep is the number of recent messages to keep after compaction.
	compactionKeep = 8
)

// CompactSession summarizes old messages via LLM and truncates history.
// No-op if session has fewer than compactionThreshold messages.
func (l *Loop) CompactSession(ctx context.Context, sessionKey string) {
	if l.sessions == nil || l.sessions.Len(sessionKey) < compactionThreshold {
		return
	}

	removed := l.sessions.Truncate(sessionKey, compactionKeep)
	if len(removed) == 0 {
		return
	}

	// Build summarization prompt.
	var sb strings.Builder
	for _, m := range removed {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
	}

	existingSummary := l.sessions.GetSummary(sessionKey)
	prompt := "Summarize this conversation concisely, preserving key facts, decisions, and action items. "
	if existingSummary != "" {
		prompt += "Previous summary:\n" + existingSummary + "\n\nNew messages:\n"
	} else {
		prompt += "Messages:\n"
	}
	prompt += sb.String()

	resp, err := l.provider.Chat(
		[]provider.Message{
			{Role: "system", Content: "You are a concise conversation summarizer. Output only the summary, no preamble."},
			{Role: "user", Content: prompt},
		},
		nil, // no tools
	)
	if err != nil {
		slog.Warn("compaction summarization failed", slog.Any("error", err))
		return
	}

	summary := strings.TrimSpace(resp.Content)
	if summary != "" {
		l.sessions.SetSummary(sessionKey, summary)
		_ = l.sessions.Save(sessionKey)
		slog.Info("session compacted", slog.String("key", sessionKey),
			slog.Int("removed", len(removed)), slog.Int("summary_len", len(summary)))
	}
}
```

**Step 4: Add auto-compaction call in Process()**

In `loop.go`, after persisting user+assistant messages (the new code from Task 2), add:

```go
	// Auto-compact when history is large.
	if l.sessions.Len(sessionKey) >= compactionThreshold {
		go l.CompactSession(ctx, sessionKey)
	}
```

**Step 5: Run all tests**

Run: `cd ~/src/dozor && go test ./internal/agent/ -v`
Expected: ALL PASS

**Step 6: Commit**

```bash
cd ~/src/dozor
git add internal/agent/compaction.go internal/agent/compaction_test.go internal/agent/loop.go
git commit -m "feat(agent): add session compaction with LLM summarization"
```

---

## Task 5: MemDB Context Injection

**Files:**
- Modify: `cmd/dozor/gateway_messages.go` (inject KB context before Process)

**Step 1: Understand current state**

`kbSearcher` is already available in `messageLoopDeps` (gateway.go:76-84). Currently unused in the message processing flow — only used in watch triage. We need to call `kbSearcher.Search()` before `Process()` and prepend the result to the user message.

**Step 2: Add trivial message detection helper**

Create `internal/agent/intent.go`:

```go
package agent

import (
	"strings"
	"unicode/utf8"
)

// trivialPrefixes are common greetings/thanks that don't need memory lookup.
var trivialPrefixes = []string{
	"привет", "здравствуй", "добрый", "hi", "hello", "hey",
	"спасибо", "thanks", "ok", "ок", "да", "нет", "yes", "no",
	"пока", "bye", "good",
}

// NeedsMemoryContext returns true if the message is substantial enough to warrant
// a knowledge base lookup. Filters out greetings, single-word messages, very short text.
func NeedsMemoryContext(text string) bool {
	if utf8.RuneCountInString(text) < 5 {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range trivialPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") && utf8.RuneCountInString(lower) < 20 {
			return false
		}
	}
	return true
}
```

Create `internal/agent/intent_test.go`:

```go
package agent

import "testing"

func TestNeedsMemoryContext(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"привет", false},
		{"hi", false},
		{"ok", false},
		{"да", false},
		{"hello there my friend", false},
		{"Почему сервер не отвечает?", true},
		{"Проверь статус nginx", true},
		{"What happened with the deploy yesterday?", true},
		{"a", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NeedsMemoryContext(tt.input)
			if got != tt.want {
				t.Errorf("NeedsMemoryContext(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
```

**Step 3: Run intent tests**

Run: `cd ~/src/dozor && go test ./internal/agent/ -run TestNeedsMemoryContext -v`
Expected: PASS

**Step 4: Modify gateway_messages.go — inject MemDB context**

In `processAgentMessage()`, before the `Process()` call:

```go
// Enrich message with KB context (long-term memory).
enrichedText := msg.Text
if deps.kbSearcher != nil && agent.NeedsMemoryContext(msg.Text) {
	kbCtx, kbCancel := context.WithTimeout(ctx, 10*time.Second)
	kbResult, err := deps.kbSearcher.Search(kbCtx, msg.Text, 3)
	kbCancel()
	if err == nil && kbResult != "" && !strings.Contains(kbResult, "No relevant") {
		enrichedText = msg.Text + "\n\n<memory_context>\n" + kbResult + "\n</memory_context>"
	}
}

response, err := deps.stack.loop.Process(ctx, msg.ChatID, enrichedText)
```

**Step 5: Run full test suite**

Run: `cd ~/src/dozor && go test ./... -v`
Expected: ALL PASS

**Step 6: Commit**

```bash
cd ~/src/dozor
git add internal/agent/intent.go internal/agent/intent_test.go cmd/dozor/gateway_messages.go
git commit -m "feat: inject MemDB long-term memory context into agent messages"
```

---

## Task 6: Auto-Save Important Interactions to MemDB

**Files:**
- Modify: `cmd/dozor/gateway_messages.go` (add post-response KB save)

**Step 1: Add auto-save logic after Process response**

In `processAgentMessage()`, after getting the response and before publishing it:

```go
// Auto-save important interactions to KB.
if deps.kbSearcher != nil && isWorthSaving(msg.Text, response) {
	go func(q, r string) {
		saveCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		content := fmt.Sprintf("user: %s\nassistant: %s", q, truncateForKB(r, 500))
		if err := deps.kbSearcher.Save(saveCtx, content); err != nil {
			slog.Warn("KB auto-save failed", slog.Any("error", err))
		}
	}(msg.Text, response)
}
```

Add helpers:

```go
// isWorthSaving returns true if the interaction contains actionable content.
func isWorthSaving(question, answer string) bool {
	if !agent.NeedsMemoryContext(question) {
		return false
	}
	// Save interactions where dozor took action or provided analysis.
	indicators := []string{"✅", "Fixed", "Restarted", "Deployed", "Error:", "Root cause", "docker", "systemctl"}
	for _, ind := range indicators {
		if strings.Contains(answer, ind) {
			return true
		}
	}
	return len(answer) > 200 // longer responses are usually analytical
}

// truncateForKB limits text length for KB storage.
func truncateForKB(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
```

**Step 2: Run full test suite**

Run: `cd ~/src/dozor && go test ./... -v`
Expected: ALL PASS

**Step 3: Commit**

```bash
cd ~/src/dozor
git add cmd/dozor/gateway_messages.go
git commit -m "feat: auto-save important interactions to MemDB"
```

---

## Task 7: Build, Deploy, Verify

**Step 1: Build**

```bash
cd ~/src/dozor
PATH=/usr/local/go/bin:$PATH go build ./cmd/dozor/
```

**Step 2: Deploy**

```bash
systemctl --user stop dozor
cp dozor ~/.local/bin/dozor
systemctl --user start dozor
```

**Step 3: Verify sessions directory created**

```bash
ls -la ~/.dozor/sessions/
```

**Step 4: Integration test via Telegram**

Send two messages to dozor via Telegram:
1. "Привет, я тестирую новую память. Меня зовут Кролик."
2. "Как меня зовут?"

Expected: Dozor should remember the name from message 1 when answering message 2.

**Step 5: Verify persistence**

```bash
# Check session file created
ls ~/.dozor/sessions/
# Check content
cat ~/.dozor/sessions/*.json | python3 -m json.tool
```

**Step 6: Verify MemDB injection**

```bash
journalctl --user -u dozor -f | grep -i "kb\|memory\|search"
```

Send a non-trivial question and check logs for KB search activity.

---

## Summary of Changes

| File | Action | Purpose |
|------|--------|---------|
| `internal/agent/session_store.go` | Create | Per-chat history storage with JSON persistence |
| `internal/agent/session_store_test.go` | Create | Tests for SessionStore |
| `internal/agent/compaction.go` | Create | LLM-based session summarization |
| `internal/agent/compaction_test.go` | Create | Tests for compaction |
| `internal/agent/intent.go` | Create | Trivial message filter for MemDB |
| `internal/agent/intent_test.go` | Create | Tests for intent detection |
| `internal/agent/loop.go` | Modify | Add sessions field, inject history in Process() |
| `internal/agent/loop_test.go` | Modify | Update Process() calls with session key |
| `cmd/dozor/setup.go` | Modify | Create SessionStore, wire to Loop |
| `cmd/dozor/gateway_messages.go` | Modify | MemDB context injection + auto-save |
| `cmd/dozor/watch.go` | Modify | Update Process() call signature |
| `internal/a2a/executor.go` | Modify | Update MessageProcessor interface |
