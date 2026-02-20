package approvals

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is how long we wait for a user to approve/deny a command.
const DefaultTimeout = 2 * time.Minute

// Status represents the outcome of an approval request.
type Status int

const (
	StatusPending  Status = iota
	StatusApproved Status = iota
	StatusDenied   Status = iota
	StatusExpired  Status = iota
)

// Request is a pending approval for a shell command.
type Request struct {
	ID        string
	Command   string
	CreatedAt time.Time
	ch        chan Status
}

// Manager tracks pending command approvals.
type Manager struct {
	mu      sync.Mutex
	pending map[string]*Request
	counter int
}

// New creates a new Manager.
func New() *Manager {
	return &Manager{pending: make(map[string]*Request)}
}

// Create registers a new pending approval and returns the request.
func (m *Manager) Create(command string) *Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	req := &Request{
		ID:        fmt.Sprintf("exec-%08d", m.counter),
		Command:   command,
		CreatedAt: time.Now(),
		ch:        make(chan Status, 1),
	}
	m.pending[req.ID] = req
	return req
}

// Resolve resolves a pending approval by ID. Returns true if found.
func (m *Manager) Resolve(id string, approved bool) bool {
	m.mu.Lock()
	req, ok := m.pending[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	delete(m.pending, id)
	m.mu.Unlock()

	if approved {
		req.ch <- StatusApproved
	} else {
		req.ch <- StatusDenied
	}
	return true
}

// Wait blocks until the request is resolved or the timeout elapses.
func (m *Manager) Wait(req *Request, timeout time.Duration) Status {
	select {
	case status := <-req.ch:
		return status
	case <-time.After(timeout):
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
		return StatusExpired
	}
}

// PendingCount returns how many approvals are currently waiting.
func (m *Manager) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// ParseResponse parses a user message like "yes exec-00000001" or "no exec-00000001".
// Returns (id, approved, ok).
func ParseResponse(text string) (id string, approved bool, ok bool) {
	text = strings.TrimSpace(text)
	var verdict, rest string
	if _, err := fmt.Sscanf(text, "%s %s", &verdict, &rest); err != nil {
		return "", false, false
	}
	verdict = strings.ToLower(strings.Trim(verdict, ".,!"))
	rest = strings.TrimSpace(rest)

	if !strings.HasPrefix(rest, "exec-") {
		return "", false, false
	}

	switch verdict {
	case "yes", "да", "approve", "y":
		return rest, true, true
	case "no", "нет", "deny", "n":
		return rest, false, true
	}
	return "", false, false
}
