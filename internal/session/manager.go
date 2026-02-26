package session

import "sync"

// Manager tracks active interactive Claude Code sessions by chat ID.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates a new session manager.
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

// Get returns the session for the given chat ID, or nil if none exists or it is closed.
func (m *Manager) Get(chatID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.sessions[chatID]
	if s != nil && s.closed.Load() {
		delete(m.sessions, chatID)
		return nil
	}
	return s
}

// Set registers a session for the given chat ID. Closes any existing session first.
func (m *Manager) Set(chatID string, s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if old := m.sessions[chatID]; old != nil {
		old.Close()
	}
	m.sessions[chatID] = s
}

// Delete closes and removes the session for the given chat ID.
func (m *Manager) Delete(chatID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s := m.sessions[chatID]; s != nil {
		s.Close()
		delete(m.sessions, chatID)
	}
}

// CloseAll closes all active sessions. Used during shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		s.Close()
		delete(m.sessions, id)
	}
}
