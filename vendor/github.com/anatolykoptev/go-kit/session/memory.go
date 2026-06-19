package session

import (
	"sync"
	"time"
)

const contentTruncSuffix = "\n... [truncated]"

// Options configures store behavior.
type Options struct {
	TTL            time.Duration // 0 = no expiry
	MaxMessages    int           // 0 = unlimited
	MaxContentSize int           // truncate message content beyond this byte length; 0 = unlimited
	MaxFacts       int           // rotate oldest facts when exceeded; 0 = unlimited
}

// InMemoryStore is a thread-safe in-memory session store.
// Uses per-key locking for better concurrency under multi-session load.
type InMemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*lockedSess
	opts     Options
}

// lockedSess pairs a session with its own mutex for per-key locking.
type lockedSess struct {
	mu   sync.RWMutex
	sess *Session
}

// NewInMemoryStore creates a new in-memory store.
func NewInMemoryStore(opts Options) *InMemoryStore {
	return &InMemoryStore{
		sessions: make(map[string]*lockedSess),
		opts:     opts,
	}
}

// getOrCreate returns an existing lockedSess or creates a new one.
// Caller must hold m.mu (at least RLock for read, Lock for create).
func (m *InMemoryStore) getOrCreateLocked(key string) *lockedSess {
	if ls, ok := m.sessions[key]; ok {
		return ls
	}
	ls := &lockedSess{sess: NewSession(key)}
	m.sessions[key] = ls
	return ls
}

// withRead locks the map for reading, finds the session, and calls fn under per-key RLock.
// Returns false if key doesn't exist.
func (m *InMemoryStore) withRead(key string, fn func(*Session)) bool {
	m.mu.RLock()
	ls, ok := m.sessions[key]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	fn(ls.sess)
	return true
}

// withWrite locks the map, gets-or-creates the session, and calls fn under per-key Lock.
func (m *InMemoryStore) withWrite(key string, fn func(*Session)) {
	m.mu.Lock()
	ls := m.getOrCreateLocked(key)
	m.mu.Unlock()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	fn(ls.sess)
}

func (m *InMemoryStore) isExpired(sess *Session) bool {
	if m.opts.TTL <= 0 {
		return false
	}
	return time.Since(sess.Updated) > m.opts.TTL
}

// truncateContent applies MaxContentSize to message content.
func (m *InMemoryStore) truncateContent(msg *Message) {
	if m.opts.MaxContentSize > 0 && len(msg.Content) > m.opts.MaxContentSize {
		msg.Content = msg.Content[:m.opts.MaxContentSize] + contentTruncSuffix
	}
}

// enforceFacts rotates oldest facts to stay within MaxFacts.
func (m *InMemoryStore) enforceFacts(sess *Session) {
	if m.opts.MaxFacts > 0 && len(sess.Facts) > m.opts.MaxFacts {
		sess.Facts = sess.Facts[len(sess.Facts)-m.opts.MaxFacts:]
	}
}

// GetOrCreate returns an existing session or creates a new one.
func (m *InMemoryStore) GetOrCreate(key string) *Session {
	m.mu.Lock()
	ls := m.getOrCreateLocked(key)
	m.mu.Unlock()
	return ls.sess
}

// AddMessage appends a message, auto-creating the session if needed.
func (m *InMemoryStore) AddMessage(key string, msg Message) {
	m.truncateContent(&msg)
	m.withWrite(key, func(sess *Session) {
		sess.AddMessage(msg)
		if m.opts.MaxMessages > 0 && len(sess.Messages) > m.opts.MaxMessages {
			sess.Messages = sess.Messages[len(sess.Messages)-m.opts.MaxMessages:]
		}
	})
}

// UpdateLastMessage replaces the content of the most recent message.
// Useful for streaming: append chunks to the last assistant message.
// No-op if the session has no messages.
func (m *InMemoryStore) UpdateLastMessage(key string, content string) {
	if m.opts.MaxContentSize > 0 && len(content) > m.opts.MaxContentSize {
		content = content[:m.opts.MaxContentSize] + contentTruncSuffix
	}
	m.withWrite(key, func(sess *Session) {
		if len(sess.Messages) == 0 {
			return
		}
		sess.Messages[len(sess.Messages)-1].Content = content
		sess.Updated = time.Now()
	})
}

// GetHistory returns a copy of messages, or nil for unknown/expired keys.
func (m *InMemoryStore) GetHistory(key string) []Message {
	var out []Message
	m.withRead(key, func(sess *Session) {
		if m.isExpired(sess) {
			return
		}
		out = make([]Message, len(sess.Messages))
		copy(out, sess.Messages)
	})
	return out
}

// GetSummary returns the summary, or "" for unknown keys.
func (m *InMemoryStore) GetSummary(key string) string {
	var s string
	m.withRead(key, func(sess *Session) { s = sess.Summary })
	return s
}

// SetSummary sets the compaction summary.
func (m *InMemoryStore) SetSummary(key, summary string) {
	m.withWrite(key, func(sess *Session) {
		sess.Summary = summary
		sess.Updated = time.Now()
	})
}

// GetFacts returns a copy of facts.
func (m *InMemoryStore) GetFacts(key string) []Fact {
	var out []Fact
	m.withRead(key, func(sess *Session) { out = sess.GetFacts() })
	return out
}

// AddFacts appends facts to a session, enforcing MaxFacts.
func (m *InMemoryStore) AddFacts(key string, facts []Fact) {
	m.withWrite(key, func(sess *Session) {
		sess.AddFacts(facts)
		m.enforceFacts(sess)
	})
}

// MessageCount returns the number of messages.
func (m *InMemoryStore) MessageCount(key string) int {
	var n int
	m.withRead(key, func(sess *Session) { n = sess.MessageCount() })
	return n
}

// CompactMessages extracts oldest messages, keeping keepLast.
func (m *InMemoryStore) CompactMessages(key string, keepLast int) []Message {
	var removed []Message
	found := m.withRead(key, func(_ *Session) {})
	if !found {
		return nil
	}
	m.withWrite(key, func(sess *Session) { removed = sess.CompactMessages(keepLast) })
	return removed
}

// TruncateHistory removes oldest messages, keeping keepLast.
func (m *InMemoryStore) TruncateHistory(key string, keepLast int) {
	m.withWrite(key, func(sess *Session) { sess.TruncateHistory(keepLast) })
}

// Clear resets a session's messages, summary, and facts.
func (m *InMemoryStore) Clear(key string) {
	m.withWrite(key, func(sess *Session) { sess.Clear() })
}

// Delete removes a session entirely.
func (m *InMemoryStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
	return nil
}

// Save is a no-op for in-memory store.
func (m *InMemoryStore) Save(_ string) error {
	return nil
}

// ListStale returns keys where Updated is older than maxAge.
func (m *InMemoryStore) ListStale(maxAge time.Duration) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoff := time.Now().Add(-maxAge)
	var keys []string
	for k, ls := range m.sessions {
		ls.mu.RLock()
		stale := ls.sess.Updated.Before(cutoff)
		ls.mu.RUnlock()
		if stale {
			keys = append(keys, k)
		}
	}
	return keys
}
