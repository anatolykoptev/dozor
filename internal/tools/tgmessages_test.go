package tools

import (
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// resetDefaultTGLog replaces the package-level log with a fresh one for test isolation.
// Caller must restore via defer.
func resetDefaultTGLog(capacity int) func() {
	old := engine.DefaultTGLog
	engine.DefaultTGLog = engine.NewTGMessageLog(capacity)
	return func() { engine.DefaultTGLog = old }
}

func TestHandleTGMessages_Defaults(t *testing.T) {
	defer resetDefaultTGLog(50)()

	// Empty input: since="" → 6h default, limit=0 → 100 default, kind="" → all.
	out, err := handleTGMessages(TGMessagesInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Messages == nil {
		t.Error("Messages must be non-nil (empty slice)")
	}
	if len(out.Messages) != 0 {
		t.Errorf("expected 0 messages from empty log, got %d", len(out.Messages))
	}
	// Verdict must mention 6h default.
	if out.Verdict == "" {
		t.Error("Verdict must not be empty")
	}
}

func TestHandleTGMessages_KindFilter(t *testing.T) {
	defer resetDefaultTGLog(50)()

	for range 4 {
		engine.DefaultTGLog.Record(engine.TGMessage{
			Kind:      engine.TGKindAlert,
			ChatID:    "c1",
			Text:      "alert",
			Timestamp: time.Now(),
		})
	}
	for range 2 {
		engine.DefaultTGLog.Record(engine.TGMessage{
			Kind:      engine.TGKindReply,
			ChatID:    "c1",
			Text:      "reply",
			Timestamp: time.Now(),
		})
	}

	// Filter for alerts only.
	out, err := handleTGMessages(TGMessagesInput{Kind: engine.TGKindAlert})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Messages) != 4 {
		t.Fatalf("expected 4 alerts, got %d", len(out.Messages))
	}
	for _, m := range out.Messages {
		if m.Kind != engine.TGKindAlert {
			t.Errorf("expected kind=alert, got %q", m.Kind)
		}
	}
}

func TestHandleTGMessages_LimitDefault(t *testing.T) {
	defer resetDefaultTGLog(200)()

	for range 150 {
		engine.DefaultTGLog.Record(engine.TGMessage{
			Kind:      engine.TGKindNotify,
			ChatID:    "c1",
			Text:      "msg",
			Timestamp: time.Now(),
		})
	}

	// limit=0 → default 100.
	out, err := handleTGMessages(TGMessagesInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Messages) != tgMessagesDefaultLimit {
		t.Fatalf("expected %d (default limit), got %d", tgMessagesDefaultLimit, len(out.Messages))
	}
}

func TestHandleTGMessages_InvalidSince(t *testing.T) {
	_, err := handleTGMessages(TGMessagesInput{Since: "1d"})
	if err == nil {
		t.Error("expected error for calendar unit '1d'")
	}
}

func TestHandleTGMessages_Readback(t *testing.T) {
	// Record to DefaultTGLog and confirm tool surfaces it.
	defer resetDefaultTGLog(20)()

	engine.DefaultTGLog.Record(engine.TGMessage{
		Kind:      engine.TGKindDeploy,
		ChatID:    "c99",
		Text:      "deploy done",
		Timestamp: time.Now(),
	})

	out, err := handleTGMessages(TGMessagesInput{Kind: engine.TGKindDeploy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 deploy message, got %d", len(out.Messages))
	}
	if out.Messages[0].Text != "deploy done" {
		t.Errorf("expected 'deploy done', got %q", out.Messages[0].Text)
	}
}
