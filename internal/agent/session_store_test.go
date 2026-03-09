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
	s1.SetSummary("key1", "test summary")
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
	if s2.GetSummary("key1") != "test summary" {
		t.Errorf("summary not persisted: got %q", s2.GetSummary("key1"))
	}
}

func TestSessionStore_Clear(t *testing.T) {
	s := NewSessionStore("")
	s.Add("k", provider.Message{Role: "user", Content: "x"})
	s.Clear("k")

	if s.Len("k") != 0 {
		t.Errorf("expected 0 after clear, got %d", s.Len("k"))
	}
	if s.Get("k") != nil {
		t.Error("expected nil after clear")
	}
}

func TestSessionStore_Truncate(t *testing.T) {
	s := NewSessionStore("")
	for i := 0; i < 10; i++ {
		s.Add("k", provider.Message{Role: "user", Content: "msg"})
	}

	removed := s.Truncate("k", 3)
	if len(removed) != 7 {
		t.Errorf("expected 7 removed, got %d", len(removed))
	}
	if s.Len("k") != 3 {
		t.Errorf("expected 3 remaining, got %d", s.Len("k"))
	}
}

func TestSessionStore_Summary(t *testing.T) {
	s := NewSessionStore("")
	if s.GetSummary("k") != "" {
		t.Error("expected empty summary for new key")
	}
	s.SetSummary("k", "compressed history")
	if s.GetSummary("k") != "compressed history" {
		t.Errorf("got %q", s.GetSummary("k"))
	}
}
