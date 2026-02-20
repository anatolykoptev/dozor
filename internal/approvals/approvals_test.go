package approvals

import (
	"testing"
	"time"
)

func TestApproveFlow(t *testing.T) {
	m := New()
	req := m.Create("ls -la /tmp")
	if req.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// Resolve asynchronously
	go func() {
		time.Sleep(10 * time.Millisecond)
		m.Resolve(req.ID, true)
	}()

	status := m.Wait(req, 5*time.Second)
	if status != StatusApproved {
		t.Errorf("expected StatusApproved, got %v", status)
	}
}

func TestDenyFlow(t *testing.T) {
	m := New()
	req := m.Create("rm -rf /tmp/test")
	go func() {
		time.Sleep(10 * time.Millisecond)
		m.Resolve(req.ID, false)
	}()
	if status := m.Wait(req, 5*time.Second); status != StatusDenied {
		t.Errorf("expected StatusDenied, got %v", status)
	}
}

func TestExpiry(t *testing.T) {
	m := New()
	req := m.Create("echo hello")
	status := m.Wait(req, 20*time.Millisecond)
	if status != StatusExpired {
		t.Errorf("expected StatusExpired, got %v", status)
	}
	if m.PendingCount() != 0 {
		t.Errorf("expected 0 pending after expiry, got %d", m.PendingCount())
	}
}

func TestParseResponse(t *testing.T) {
	cases := []struct {
		input    string
		wantID   string
		wantOK   bool
		wantAppr bool
	}{
		{"yes exec-00000001", "exec-00000001", true, true},
		{"no exec-00000002", "exec-00000002", true, false},
		{"да exec-00000003", "exec-00000003", true, true},
		{"нет exec-00000004", "exec-00000004", true, false},
		{"YES exec-00000005", "exec-00000005", true, true},
		{"⏳ Принял", "", false, false},
		{"random text", "", false, false},
		{"yes notanid", "", false, false},
	}
	for _, c := range cases {
		id, approved, ok := ParseResponse(c.input)
		if ok != c.wantOK {
			t.Errorf("ParseResponse(%q): ok=%v want %v", c.input, ok, c.wantOK)
			continue
		}
		if ok && id != c.wantID {
			t.Errorf("ParseResponse(%q): id=%q want %q", c.input, id, c.wantID)
		}
		if ok && approved != c.wantAppr {
			t.Errorf("ParseResponse(%q): approved=%v want %v", c.input, approved, c.wantAppr)
		}
	}
}
