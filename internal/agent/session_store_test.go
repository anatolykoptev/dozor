package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anatolykoptev/dozor/internal/provider"
)

func TestSessionStore_AddGet(t *testing.T) {
	s := NewSessionStore("")
	s.Add("chat1", provider.Message{Role: "user", Content: "hello"})
	s.Add("chat1", provider.Message{Role: "assistant", Content: "hi"})

	msgs := s.Get("chat1")
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Content != "hello" || msgs[1].Content != "hi" {
		t.Errorf("unexpected messages: %+v", msgs)
	}
}

func TestSessionStore_GetEmpty(t *testing.T) {
	s := NewSessionStore("")
	msgs := s.Get("nonexistent")
	if msgs != nil {
		t.Errorf("expected nil for nonexistent key, got %v", msgs)
	}
}

func TestSessionStore_IsolatedSessions(t *testing.T) {
	s := NewSessionStore("")
	s.Add("a", provider.Message{Role: "user", Content: "msg-a"})
	s.Add("b", provider.Message{Role: "user", Content: "msg-b"})

	if s.Len("a") != 1 || s.Len("b") != 1 {
		t.Errorf("sessions not isolated: a=%d, b=%d", s.Len("a"), s.Len("b"))
	}
	if s.Get("a")[0].Content != "msg-a" {
		t.Error("session a has wrong content")
	}
	if s.Get("b")[0].Content != "msg-b" {
		t.Error("session b has wrong content")
	}
}

func TestSessionStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	// Write session.
	s1 := NewSessionStore(dir)
	s1.Add("key1", provider.Message{Role: "user", Content: "persisted"})
	if err := s1.Save("key1"); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Verify file exists.
	files, _ := os.ReadDir(dir)
	found := false
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".json" {
			found = true
		}
	}
	if !found {
		t.Fatal("no JSON file created in session dir")
	}

	// Load into new store.
	s2 := NewSessionStore(dir)
	msgs := s2.Get("key1")
	if len(msgs) != 1 || msgs[0].Content != "persisted" {
		t.Errorf("persistence failed: got %+v", msgs)
	}
}

func TestSessionStore_Summary(t *testing.T) {
	s := NewSessionStore("")
	if s.GetSummary("k") != "" {
		t.Error("expected empty summary for new key")
	}
}

func TestSessionStore_ToolCallRoundtrip(t *testing.T) {
	s := NewSessionStore("")
	orig := provider.Message{
		Role:       "assistant",
		Content:    "",
		ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "my_tool", Args: map[string]any{"x": float64(1)}},
		},
	}
	s.Add("chat1", orig)

	msgs := s.Get("chat1")
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(msgs[0].ToolCalls))
	}
	tc := msgs[0].ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "my_tool" {
		t.Errorf("tool call id/name mismatch: %+v", tc)
	}
	if tc.Args["x"] != float64(1) {
		t.Errorf("tool call args mismatch: %+v", tc.Args)
	}
}
