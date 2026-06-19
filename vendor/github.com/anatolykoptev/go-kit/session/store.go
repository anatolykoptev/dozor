package session

import "time"

// Store manages per-key conversation sessions with pluggable backends.
type Store interface {
	// GetOrCreate returns an existing session or creates a new one.
	GetOrCreate(key string) *Session

	// AddMessage appends a message to the session's history.
	AddMessage(key string, msg Message)

	// UpdateLastMessage replaces the content of the most recent message.
	// Useful for streaming LLM responses: buffer chunks into the last message.
	UpdateLastMessage(key string, content string)

	// GetHistory returns an ordered copy of the session's messages.
	GetHistory(key string) []Message

	// GetSummary returns the compaction summary for a session.
	GetSummary(key string) string

	// SetSummary stores a compaction summary.
	SetSummary(key, summary string)

	// GetFacts returns a copy of extracted facts.
	GetFacts(key string) []Fact

	// AddFacts appends new facts to a session.
	AddFacts(key string, facts []Fact)

	// MessageCount returns the number of messages in a session.
	MessageCount(key string) int

	// CompactMessages extracts the oldest messages, keeping keepLast.
	// Returns the extracted messages without modifying the caller's view.
	CompactMessages(key string, keepLast int) []Message

	// TruncateHistory removes the oldest messages, keeping keepLast.
	TruncateHistory(key string, keepLast int)

	// Clear resets a session's messages, summary, and facts.
	Clear(key string)

	// Delete removes a session entirely.
	Delete(key string) error

	// Save persists a session to the backend's storage.
	Save(key string) error

	// ListStale returns session keys where Updated is older than maxAge.
	ListStale(maxAge time.Duration) []string
}
