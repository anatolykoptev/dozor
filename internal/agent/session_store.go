package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anatolykoptev/dozor/internal/provider"
)

const (
	dirPerm  = 0700
	filePerm = 0600
)

type sessionData struct {
	Messages []provider.Message `json:"messages"`
	Summary  string             `json:"summary,omitempty"`
}

// SessionStore manages per-key conversation history with optional file persistence.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*sessionData
	dir      string
}

// NewSessionStore creates a new session store. If dir is non-empty, sessions are
// persisted to JSON files and loaded on startup.
func NewSessionStore(dir string) *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*sessionData),
		dir:      dir,
	}
	if dir != "" {
		_ = os.MkdirAll(dir, dirPerm)
		s.loadAll()
	}
	return s
}

// Add appends a message to the session history.
func (s *SessionStore) Add(key string, msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd := s.getOrCreate(key)
	sd.Messages = append(sd.Messages, msg)
}

// Get returns a copy of the session's message history.
func (s *SessionStore) Get(key string) []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sd, ok := s.sessions[key]
	if !ok {
		return nil
	}
	out := make([]provider.Message, len(sd.Messages))
	copy(out, sd.Messages)
	return out
}

// GetSummary returns the compaction summary for a session.
func (s *SessionStore) GetSummary(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sd, ok := s.sessions[key]; ok {
		return sd.Summary
	}
	return ""
}

// SetSummary stores a compaction summary for a session.
func (s *SessionStore) SetSummary(key, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd := s.getOrCreate(key)
	sd.Summary = summary
}

// Len returns the number of messages in a session.
func (s *SessionStore) Len(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sd, ok := s.sessions[key]; ok {
		return len(sd.Messages)
	}
	return 0
}

// Truncate removes the oldest messages, keeping only keepLast.
// Returns the removed messages (for compaction).
func (s *SessionStore) Truncate(key string, keepLast int) []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd, ok := s.sessions[key]
	if !ok || len(sd.Messages) <= keepLast {
		return nil
	}
	cutoff := len(sd.Messages) - keepLast
	removed := make([]provider.Message, cutoff)
	copy(removed, sd.Messages[:cutoff])
	sd.Messages = sd.Messages[cutoff:]
	return removed
}

// Clear removes all data for a session key.
func (s *SessionStore) Clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// Save persists a session to disk. No-op if dir is empty.
func (s *SessionStore) Save(key string) error {
	if s.dir == "" {
		return nil
	}
	s.mu.RLock()
	sd, ok := s.sessions[key]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	data, err := json.Marshal(sd)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	path := s.filePath(key)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, filePerm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *SessionStore) getOrCreate(key string) *sessionData {
	sd, ok := s.sessions[key]
	if !ok {
		sd = &sessionData{}
		s.sessions[key] = sd
	}
	return sd
}

func (s *SessionStore) filePath(key string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(key)
	return filepath.Join(s.dir, safe+".json")
}

func (s *SessionStore) loadAll() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sd sessionData
		if err := json.Unmarshal(data, &sd); err != nil {
			slog.Debug("skip corrupt session file",
				slog.String("file", e.Name()))
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".json")
		s.sessions[key] = &sd
	}
}
