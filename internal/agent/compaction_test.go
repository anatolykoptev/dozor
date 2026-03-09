package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/provider"
)

func TestCompact_UnderThreshold(t *testing.T) {
	store := NewSessionStore("")

	called := false
	store.WithCompactor(func(_ context.Context, _ string) (string, error) {
		called = true
		return "summary", nil
	})

	for range 5 {
		store.Add("chat1", provider.Message{Role: "user", Content: "msg"})
	}

	store.Compact(context.Background(), "chat1")

	if called {
		t.Error("summarize should not be called when under threshold")
	}
}

func TestCompact_OverThreshold(t *testing.T) {
	store := NewSessionStore("")

	var gotPrompt string
	store.WithCompactor(func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "Summary: user sent many test messages", nil
	})

	threshold, keep := compactionConfig()
	for range threshold + 5 {
		store.Add("chat1", provider.Message{
			Role:    "user",
			Content: "message " + strings.Repeat("x", 10),
		})
	}

	store.Compact(context.Background(), "chat1")

	if gotPrompt == "" {
		t.Error("summarize should be called when over threshold")
	}

	summary := store.GetSummary("chat1")
	if summary == "" {
		t.Error("summary should be set after compaction")
	}

	if store.Len("chat1") != keep {
		t.Errorf("history should have %d messages, got %d", keep, store.Len("chat1"))
	}
}

func TestCompact_NoCompactor(t *testing.T) {
	store := NewSessionStore("")
	threshold, _ := compactionConfig()
	for range threshold + 5 {
		store.Add("chat1", provider.Message{Role: "user", Content: "msg"})
	}

	// Must not panic when no compactor attached.
	store.Compact(context.Background(), "chat1")
}
