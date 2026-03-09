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

	if p.callCount != 1 {
		t.Errorf("provider should be called once, got %d", p.callCount)
	}

	summary := store.GetSummary("chat1")
	if summary == "" {
		t.Error("summary should be set after compaction")
	}

	if store.Len("chat1") != compactionKeep {
		t.Errorf("history should have %d messages, got %d", compactionKeep, store.Len("chat1"))
	}
}
