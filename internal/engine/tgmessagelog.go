package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultTGLogCapacity is the number of TGMessage entries retained in the ring buffer.
// 500 entries cover roughly 24h of typical alert + reply volume without significant
// memory overhead (~1 KB per truncated entry × 500 = ~500 KB worst-case).
const defaultTGLogCapacity = 500

// maxTGMessageText is the maximum number of UTF-8 characters stored per message.
// Agent replies and alert descriptions can be very long; truncation keeps the
// persistence file bounded. Telegram's own hard limit for a single message is 4096
// characters; we use that as the stored cap.
const maxTGMessageText = 4000

// tgRecentDefaultLimit caps the number of messages returned by Recent when limit <= 0.
const tgRecentDefaultLimit = 100

// tgDefaultSince is the default lookback window when since <= 0 in Recent.
const tgDefaultSince = 6 * time.Hour

// TGKind values returned by ClassifyKind.
const (
	TGKindAlert   = "alert"
	TGKindNotify  = "notify"
	TGKindReply   = "reply"
	TGKindAck     = "ack"
	TGKindSession = "session"
	TGKindDeploy  = "deploy"
	TGKindOther   = "other"
)

// TGMessage is a durable record of a single message successfully delivered to Telegram.
//
// It is the canonical history of ALL Telegram traffic from dozor — alerts, agent
// replies, acks, session messages, deploy notifications. Photo bytes are intentionally
// NOT stored (they would bloat the file); only HasPhoto signals their presence.
// Text is truncated to maxTGMessageText characters to bound file size; long agent
// replies lose trailing content but remain identifiable.
type TGMessage struct {
	// Kind is the message type classified from the bus.Message ID prefix.
	// Values: alert, notify, reply, ack, session, deploy, other.
	Kind string `json:"kind"`

	// ChatID is the Telegram chat identifier this message was delivered to.
	ChatID string `json:"chat_id"`

	// Text is the message text (or photo caption), truncated to maxTGMessageText chars.
	Text string `json:"text,omitempty"`

	// HasPhoto is true when the message carried a PNG photo attachment (e.g. an alert card).
	// The photo bytes themselves are not persisted.
	HasPhoto bool `json:"has_photo,omitempty"`

	// Timestamp is the wall-clock time when dozor successfully delivered this message
	// to Telegram (delivery time, not the source event time).
	Timestamp time.Time `json:"timestamp"`
}

// tgPersistFile is the JSON document written to disk.
type tgPersistFile struct {
	Messages []TGMessage `json:"messages"`
}

// TGMessageLog is a fixed-capacity, thread-safe circular buffer of TGMessage values
// with optional atomic JSON persistence.
//
// Designed as the durable history of all Telegram traffic from dozor. Alerts are
// included (they flow through the same sendReply funnel). The ring is bounded so
// old entries are overwritten once full. The persistence file survives process restart;
// the ring is re-populated from it at boot via BindPersistence.
//
// Do not use the zero value; use NewTGMessageLog.
type TGMessageLog struct {
	mu          sync.Mutex
	buf         []TGMessage
	next        int // index of next write slot
	size        int // current number of valid entries (≤ capacity)
	capacity    int
	persistPath string
}

// NewTGMessageLog returns a TGMessageLog with the given capacity.
// capacity <= 0 uses defaultTGLogCapacity.
func NewTGMessageLog(capacity int) *TGMessageLog {
	if capacity <= 0 {
		capacity = defaultTGLogCapacity
	}
	return &TGMessageLog{
		buf:      make([]TGMessage, capacity),
		capacity: capacity,
	}
}

// DefaultTGLog is the package-level singleton used by internal/telegram/telegram.go
// (via sendReply) and by the tg-messages MCP tool.
var DefaultTGLog = NewTGMessageLog(defaultTGLogCapacity)

// BindPersistence sets the file path for durable snapshots and loads any existing
// persisted messages into the ring. A missing or corrupt state file is tolerated —
// it is logged and the ring starts empty. Dozor must boot regardless of persistence state.
func (l *TGMessageLog) BindPersistence(path string) {
	l.mu.Lock()
	l.persistPath = path
	l.mu.Unlock()

	// Load outside the lock — we call the public method which re-acquires.
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("tg-message-log: cannot read persist file, starting empty",
				slog.String("path", path), slog.Any("error", err))
		}
		return
	}

	var doc tgPersistFile
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Warn("tg-message-log: corrupt persist file, starting empty",
			slog.String("path", path), slog.Any("error", err))
		return
	}

	// Re-insert in file order (oldest first) so the ring ends up with the newest
	// entries surviving when the count exceeds capacity.
	l.mu.Lock()
	for _, m := range doc.Messages {
		l.insertLocked(m)
	}
	l.mu.Unlock()

	slog.Info("tg-message-log: loaded from persist file",
		slog.String("path", path), slog.Int("count", len(doc.Messages)))
}

// Record appends m to the ring and flushes to disk (best-effort). If m.Timestamp
// is zero it is set to time.Now() before storage. Record never blocks the caller
// on disk I/O; a flush failure is logged at Warn level and never propagated.
func (l *TGMessageLog) Record(m TGMessage) {
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	// Truncate text to bound file size.
	if len([]rune(m.Text)) > maxTGMessageText {
		runes := []rune(m.Text)
		m.Text = string(runes[:maxTGMessageText])
	}

	l.mu.Lock()
	l.insertLocked(m)
	path := l.persistPath
	snapshot := l.snapshotLocked()
	l.mu.Unlock()

	if path != "" {
		if err := writeTGJSONAtomic(path, tgPersistFile{Messages: snapshot}); err != nil {
			slog.Warn("tg-message-log: persist flush failed",
				slog.String("path", path), slog.Any("error", err))
		}
	}
}

// insertLocked appends m to the ring, overwriting the oldest entry when full.
// MUST be called with l.mu held.
func (l *TGMessageLog) insertLocked(m TGMessage) {
	l.buf[l.next] = m
	l.next = (l.next + 1) % l.capacity
	if l.size < l.capacity {
		l.size++
	}
}

// snapshotLocked returns all valid entries in oldest-first order (suitable for
// writing to the persist file). MUST be called with l.mu held.
func (l *TGMessageLog) snapshotLocked() []TGMessage {
	if l.size == 0 {
		return nil
	}
	out := make([]TGMessage, l.size)
	for i := range l.size {
		// Translate logical oldest-first index to physical buffer index.
		idx := (l.next - l.size + i + l.capacity) % l.capacity
		out[i] = l.buf[idx]
	}
	return out
}

// Recent returns messages whose Timestamp >= now-since, newest-first, capped at limit.
//   - since <= 0: no time filter (all entries returned).
//   - limit <= 0: uses tgRecentDefaultLimit.
//   - kind != "": exact match filter on TGMessage.Kind; empty means all kinds.
func (l *TGMessageLog) Recent(since time.Duration, limit int, kind string) []TGMessage {
	if limit <= 0 {
		limit = tgRecentDefaultLimit
	}

	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.size == 0 {
		return nil
	}

	out := make([]TGMessage, 0, min(l.size, limit))
	for i := range l.size {
		// Walk newest-first: slot just before l.next is the most recent write.
		idx := (l.next - 1 - i + l.capacity) % l.capacity
		m := l.buf[idx]
		if !cutoff.IsZero() && m.Timestamp.Before(cutoff) {
			continue
		}
		if kind != "" && m.Kind != kind {
			continue
		}
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Flush forces an atomic snapshot write to disk. Safe to call from a graceful-
// shutdown defer. Returns nil when persistPath is not set.
func (l *TGMessageLog) Flush() error {
	l.mu.Lock()
	path := l.persistPath
	snapshot := l.snapshotLocked()
	l.mu.Unlock()

	if path == "" {
		return nil
	}
	return writeTGJSONAtomic(path, tgPersistFile{Messages: snapshot})
}

// DefaultTGLogPath returns the path for the durable message log, mirroring the
// deploy-queue/debounce pattern: $DOZOR_WORKSPACE if set, else ~/.dozor/.
func DefaultTGLogPath() string {
	ws := os.Getenv("DOZOR_WORKSPACE")
	if ws == "" {
		home, _ := os.UserHomeDir()
		ws = filepath.Join(home, ".dozor")
	}
	return filepath.Join(ws, "tg-messages.json")
}

// ClassifyKind maps a bus.Message ID to one of the canonical kind strings.
// Rules (applied in order — first match wins):
//
//	"alert-"   prefix           → alert
//	"notify-"  prefix           → notify
//	"deploy"   prefix           → deploy
//	"session-" prefix           → session
//	"-reply"   suffix           → reply
//	"-ack"     suffix           → ack  (covers -approval-ack and plain -ack)
//	"-session" anywhere         → session  (e.g. "<id>-session-xyz")
//	else                        → other
func ClassifyKind(id string) string {
	switch {
	case strings.HasPrefix(id, "alert-"):
		return TGKindAlert
	case strings.HasPrefix(id, "notify-"):
		return TGKindNotify
	case strings.HasPrefix(id, "deploy"):
		return TGKindDeploy
	case strings.HasPrefix(id, "session-"):
		return TGKindSession
	case strings.HasSuffix(id, "-reply"):
		return TGKindReply
	case strings.HasSuffix(id, "-ack"):
		return TGKindAck
	case strings.Contains(id, "-session"):
		return TGKindSession
	default:
		return TGKindOther
	}
}

// writeTGJSONAtomic marshals v to path via tmp-file + rename so a concurrent reader
// never sees a partial document. MkdirAll creates parent directories with mode 0750.
// This is a local copy of the deploy-package pattern; we do NOT import internal/deploy
// (wrong layer — would create an undesired dependency from engine toward deploy).
func writeTGJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:mnd // standard workspace dir mode
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tg-messages-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck // returning write error
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp into place: %w", err)
	}
	return nil
}
