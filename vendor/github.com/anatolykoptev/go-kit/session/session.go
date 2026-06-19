package session

import "time"

// Fact is a single extracted fact from a compacted conversation segment.
type Fact struct {
	Content     string    `json:"content"`
	ExtractedAt time.Time `json:"extracted_at"`
}

// Session holds conversation state for a single key.
type Session struct {
	Key      string    `json:"key"`
	Messages []Message `json:"messages"`
	Summary  string    `json:"summary,omitempty"`
	Facts    []Fact    `json:"facts,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// NewSession creates a new session with the given key.
func NewSession(key string) *Session {
	now := time.Now()
	return &Session{
		Key:      key,
		Messages: []Message{},
		Created:  now,
		Updated:  now,
	}
}

// AddMessage appends a message and updates the timestamp.
func (s *Session) AddMessage(msg Message) {
	s.Messages = append(s.Messages, msg)
	s.Updated = time.Now()
}

// MessageCount returns the number of messages.
func (s *Session) MessageCount() int {
	return len(s.Messages)
}

// CompactMessages extracts the oldest messages, keeping the last keepLast.
// Returns nil if there are fewer messages than keepLast.
func (s *Session) CompactMessages(keepLast int) []Message {
	if len(s.Messages) <= keepLast {
		return nil
	}
	cutoff := len(s.Messages) - keepLast
	removed := make([]Message, cutoff)
	copy(removed, s.Messages[:cutoff])
	s.Messages = s.Messages[cutoff:]
	s.Updated = time.Now()
	return removed
}

// TruncateHistory removes the oldest messages, keeping the last keepLast.
func (s *Session) TruncateHistory(keepLast int) {
	if len(s.Messages) > keepLast {
		s.Messages = s.Messages[len(s.Messages)-keepLast:]
		s.Updated = time.Now()
	}
}

// Clear resets messages, summary, and facts.
func (s *Session) Clear() {
	s.Messages = nil
	s.Summary = ""
	s.Facts = nil
	s.Updated = time.Now()
}

// GetFacts returns a copy of the facts slice.
func (s *Session) GetFacts() []Fact {
	if len(s.Facts) == 0 {
		return nil
	}
	out := make([]Fact, len(s.Facts))
	copy(out, s.Facts)
	return out
}

// AddFacts appends facts to the session.
func (s *Session) AddFacts(facts []Fact) {
	s.Facts = append(s.Facts, facts...)
	s.Updated = time.Now()
}
