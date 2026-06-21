package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeTGMsg(kind, chatID, text string) TGMessage {
	return TGMessage{
		Kind:      kind,
		ChatID:    chatID,
		Text:      text,
		Timestamp: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Ring: wraparound
// ---------------------------------------------------------------------------

func TestTGMessageLog_Wraparound(t *testing.T) {
	l := NewTGMessageLog(3)
	for i := range 5 {
		m := makeTGMsg(TGKindAlert, "c1", "msg")
		m.Text = string(rune('a' + i)) // distinguishable text
		l.Record(m)
	}
	got := l.Recent(0, 0, "")
	if len(got) != 3 {
		t.Fatalf("expected 3 (capacity), got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Ring: time filter
// ---------------------------------------------------------------------------

func TestTGMessageLog_RecentTimeFilter(t *testing.T) {
	l := NewTGMessageLog(10)
	now := time.Now()

	old := makeTGMsg(TGKindNotify, "c1", "old msg")
	old.Timestamp = now.Add(-2 * time.Hour)
	l.mu.Lock()
	l.insertLocked(old)
	l.mu.Unlock()

	fresh := makeTGMsg(TGKindNotify, "c1", "fresh msg")
	fresh.Timestamp = now.Add(-5 * time.Minute)
	l.mu.Lock()
	l.insertLocked(fresh)
	l.mu.Unlock()

	got := l.Recent(time.Hour, 0, "")
	if len(got) != 1 {
		t.Fatalf("expected 1 after 1h filter, got %d", len(got))
	}
	if got[0].Text != "fresh msg" {
		t.Errorf("expected fresh msg, got %q", got[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Ring: kind filter
// ---------------------------------------------------------------------------

func TestTGMessageLog_RecentKindFilter(t *testing.T) {
	l := NewTGMessageLog(20)
	for range 5 {
		l.Record(makeTGMsg(TGKindAlert, "c1", "alert"))
	}
	for range 3 {
		l.Record(makeTGMsg(TGKindReply, "c1", "reply"))
	}

	alerts := l.Recent(0, 0, TGKindAlert)
	if len(alerts) != 5 {
		t.Fatalf("expected 5 alerts, got %d", len(alerts))
	}
	replies := l.Recent(0, 0, TGKindReply)
	if len(replies) != 3 {
		t.Fatalf("expected 3 replies, got %d", len(replies))
	}
	all := l.Recent(0, 0, "")
	if len(all) != 8 {
		t.Fatalf("expected 8 total, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// Ring: newest-first order
// ---------------------------------------------------------------------------

func TestTGMessageLog_NewestFirst(t *testing.T) {
	l := NewTGMessageLog(10)
	base := time.Now().Add(-5 * time.Minute)
	texts := []string{"first", "second", "third"}
	for i, txt := range texts {
		m := makeTGMsg(TGKindNotify, "c1", txt)
		m.Timestamp = base.Add(time.Duration(i) * time.Minute)
		l.mu.Lock()
		l.insertLocked(m)
		l.mu.Unlock()
	}

	got := l.Recent(0, 10, "")
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// Newest-first: third, second, first.
	want := []string{"third", "second", "first"}
	for i, m := range got {
		if m.Text != want[i] {
			t.Errorf("idx %d: want %q, got %q", i, want[i], m.Text)
		}
	}
}

// ---------------------------------------------------------------------------
// Ring: limit cap
// ---------------------------------------------------------------------------

func TestTGMessageLog_LimitCap(t *testing.T) {
	l := NewTGMessageLog(50)
	for range 30 {
		l.Record(makeTGMsg(TGKindAlert, "c1", "x"))
	}
	got := l.Recent(0, 5, "")
	if len(got) != 5 {
		t.Fatalf("expected limit=5, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Record: stamps zero timestamp
// ---------------------------------------------------------------------------

func TestTGMessageLog_RecordStampsZeroTimestamp(t *testing.T) {
	l := NewTGMessageLog(5)
	m := TGMessage{
		Kind:   TGKindAlert,
		ChatID: "c1",
		Text:   "msg",
		// Timestamp deliberately zero
	}
	before := time.Now()
	l.Record(m)
	after := time.Now()

	got := l.Recent(0, 1, "")
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	ts := got[0].Timestamp
	if ts.IsZero() {
		t.Error("Timestamp must not be zero after Record")
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("Timestamp %v not in [%v, %v]", ts, before, after)
	}
}

// ---------------------------------------------------------------------------
// Record: text truncation
// ---------------------------------------------------------------------------

func TestTGMessageLog_TextTruncation(t *testing.T) {
	l := NewTGMessageLog(5)
	// Build a string longer than maxTGMessageText runes.
	long := make([]rune, maxTGMessageText+100)
	for i := range long {
		long[i] = 'A'
	}
	m := makeTGMsg(TGKindReply, "c1", string(long))
	l.Record(m)

	got := l.Recent(0, 1, "")
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	stored := []rune(got[0].Text)
	if len(stored) != maxTGMessageText {
		t.Errorf("expected %d runes after truncation, got %d", maxTGMessageText, len(stored))
	}
}

// ---------------------------------------------------------------------------
// ClassifyKind table
// ---------------------------------------------------------------------------

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"alert-1719000000000", TGKindAlert},
		{"alert-xyz", TGKindAlert},
		{"notify-1719000000001", TGKindNotify},
		{"notify-boot", TGKindNotify},
		{"deploy", TGKindDeploy},
		{"deploy-webhook-start", TGKindDeploy},
		// suffix wins over the deploy prefix — a deploy-subsystem ack/reply is
		// classified by its message type, not its origin.
		{"deploy-webhook-ack", TGKindAck},
		{"deploy-build-reply", TGKindReply},
		{"session-abc123", TGKindSession},
		{"abc123-reply", TGKindReply},
		{"msg999-reply", TGKindReply},
		{"abc123-ack", TGKindAck},
		{"abc123-approval-ack", TGKindAck},
		{"abc123-session-xyz", TGKindSession},
		{"abc123-session", TGKindSession},
		{"random-string", TGKindOther},
		{"", TGKindOther},
		{"tg-12345", TGKindOther},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got := ClassifyKind(tc.id)
			if got != tc.want {
				t.Errorf("ClassifyKind(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Persistence: roundtrip
// ---------------------------------------------------------------------------

func TestTGMessageLog_PersistRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tg-messages.json")

	// Write log.
	l1 := NewTGMessageLog(50)
	l1.BindPersistence(path)
	msgs := []TGMessage{
		makeTGMsg(TGKindAlert, "c1", "alert text"),
		makeTGMsg(TGKindReply, "c2", "reply text"),
		makeTGMsg(TGKindDeploy, "c1", "deploy done"),
	}
	for _, m := range msgs {
		l1.Record(m)
	}
	if err := l1.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Read back via new log with same path.
	l2 := NewTGMessageLog(50)
	l2.BindPersistence(path)

	got := l2.Recent(0, 0, "")
	if len(got) != len(msgs) {
		t.Fatalf("expected %d messages after reload, got %d", len(msgs), len(got))
	}
	// Recent returns newest-first; verify all kinds present.
	kinds := make(map[string]bool)
	for _, m := range got {
		kinds[m.Kind] = true
	}
	for _, m := range msgs {
		if !kinds[m.Kind] {
			t.Errorf("kind %q missing after reload", m.Kind)
		}
	}
}

// ---------------------------------------------------------------------------
// Persistence: corrupt file tolerated
// ---------------------------------------------------------------------------

func TestTGMessageLog_CorruptFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tg-messages.json")

	// Write garbage.
	if err := os.WriteFile(path, []byte("this is not json {{{"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	l := NewTGMessageLog(10)
	// Must not panic or return error — starts empty.
	l.BindPersistence(path)

	got := l.Recent(0, 0, "")
	if len(got) != 0 {
		t.Errorf("expected empty ring after corrupt file, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Persistence: missing file tolerated
// ---------------------------------------------------------------------------

func TestTGMessageLog_MissingFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	l := NewTGMessageLog(10)
	l.BindPersistence(path) // must not panic
	got := l.Recent(0, 0, "")
	if got != nil {
		t.Errorf("expected nil from empty log, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrent Record (race detector)
// ---------------------------------------------------------------------------

func TestTGMessageLog_ConcurrentRecord(t *testing.T) {
	// Bind persistence so the concurrent goroutines also exercise the disk-flush
	// path (Record → writeTGJSONAtomic) under -race, not just the in-memory ring.
	l := NewTGMessageLog(20)
	l.BindPersistence(filepath.Join(t.TempDir(), "tg-messages.json"))
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Record(makeTGMsg(TGKindAlert, "c1", "concurrent"))
			_ = l.Recent(time.Hour, 10, "")
			_ = l.Flush()
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Persistence: file is valid JSON with expected shape
// ---------------------------------------------------------------------------

func TestTGMessageLog_PersistFileShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tg-messages.json")

	l := NewTGMessageLog(10)
	l.BindPersistence(path)
	l.Record(makeTGMsg(TGKindNotify, "c1", "hello"))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persist file: %v", err)
	}
	var doc struct {
		Messages []TGMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse persist file: %v", err)
	}
	if len(doc.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(doc.Messages))
	}
	if doc.Messages[0].Kind != TGKindNotify {
		t.Errorf("expected kind notify, got %q", doc.Messages[0].Kind)
	}
}
