package engine

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
)

// mockEventsClient simulates the Docker Events API for testing.
type mockEventsClient struct {
	messages []events.Message
	err      error
}

func (m *mockEventsClient) Events(_ context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	msgCh := make(chan events.Message, len(m.messages))
	errCh := make(chan error, 1)

	for _, msg := range m.messages {
		msgCh <- msg
	}
	close(msgCh)

	if m.err != nil {
		errCh <- m.err
	}
	close(errCh)

	return msgCh, errCh
}

func makeContainerEvent(action events.Action, containerName string, ts time.Time) events.Message {
	return events.Message{
		Type:   events.ContainerEventType,
		Action: action,
		Actor: events.Actor{
			Attributes: map[string]string{"name": containerName},
		},
		Time: ts.Unix(),
	}
}

func TestRestartsInWindow_NoEvents(t *testing.T) {
	cli := &mockEventsClient{}
	count := RestartsInWindow(context.Background(), cli, "go-hully", 24*time.Hour)
	if count != 0 {
		t.Errorf("expected 0 restarts, got %d", count)
	}
}

func TestRestartsInWindow_EventsWithinWindow(t *testing.T) {
	now := time.Now()
	cli := &mockEventsClient{
		messages: []events.Message{
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-2*time.Hour)),
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-5*time.Hour)),
		},
	}
	count := RestartsInWindow(context.Background(), cli, "go-hully", 24*time.Hour)
	if count != 2 {
		t.Errorf("expected 2 restarts, got %d", count)
	}
}

func TestRestartsInWindow_EventsOutsideWindow(t *testing.T) {
	now := time.Now()
	// Events older than 24h should not be counted (before the since boundary).
	cli := &mockEventsClient{
		messages: []events.Message{
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-25*time.Hour)),
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-48*time.Hour)),
		},
	}
	count := RestartsInWindow(context.Background(), cli, "go-hully", 24*time.Hour)
	if count != 0 {
		t.Errorf("expected 0 restarts for old events, got %d", count)
	}
}

func TestRestartsInWindow_MixedEvents(t *testing.T) {
	now := time.Now()
	cli := &mockEventsClient{
		messages: []events.Message{
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-1*time.Hour)),  // within
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-23*time.Hour)), // within
			makeContainerEvent(events.ActionDie, "go-hully", now.Add(-25*time.Hour)), // outside
		},
	}
	count := RestartsInWindow(context.Background(), cli, "go-hully", 24*time.Hour)
	if count != 2 {
		t.Errorf("expected 2 restarts, got %d", count)
	}
}

func TestRestartsInWindow_ErrorFromAPI(t *testing.T) {
	// Even if Events API returns an error, we should get 0 safely (not panic).
	cli := &mockEventsClient{
		messages: nil,
		err:      context.DeadlineExceeded,
	}
	count := RestartsInWindow(context.Background(), cli, "go-hully", 24*time.Hour)
	if count != 0 {
		t.Errorf("expected 0 on API error, got %d", count)
	}
}

func TestServiceStatus_IsHealthy_UsesRecentRestarts(t *testing.T) {
	t.Run("no recent restarts — healthy despite high total", func(t *testing.T) {
		s := ServiceStatus{
			Name:           "go-hully",
			State:          StateRunning,
			RestartCount:   4, // historical, irrelevant
			RecentRestarts: 0,
			ErrorCount:     0,
		}
		if !s.IsHealthy() {
			t.Error("expected healthy: no recent restarts and running")
		}
	})

	t.Run("recent restarts at threshold — unhealthy", func(t *testing.T) {
		s := ServiceStatus{
			Name:           "go-hully",
			State:          StateRunning,
			RestartCount:   4,
			RecentRestarts: recentRestartThreshold,
			ErrorCount:     0,
		}
		if s.IsHealthy() {
			t.Error("expected unhealthy: recent restarts at threshold")
		}
	})

	t.Run("one recent restart — still healthy", func(t *testing.T) {
		s := ServiceStatus{
			Name:           "svc",
			State:          StateRunning,
			RecentRestarts: 1,
			ErrorCount:     0,
		}
		if !s.IsHealthy() {
			t.Error("expected healthy: single restart (e.g. deploy) should not degrade")
		}
	})
}

func TestServiceStatus_GetAlertLevel_UsesRecentRestarts(t *testing.T) {
	t.Run("zero recent restarts — no error alert from restarts", func(t *testing.T) {
		s := ServiceStatus{
			State:          StateRunning,
			RestartCount:   5,
			RecentRestarts: 0,
		}
		level := s.GetAlertLevel()
		if level == AlertError {
			t.Errorf("expected no AlertError for zero recent restarts, got %s", level)
		}
	})

	t.Run("recent restarts >= threshold — AlertError", func(t *testing.T) {
		s := ServiceStatus{
			State:          StateRunning,
			RecentRestarts: recentRestartThreshold,
		}
		level := s.GetAlertLevel()
		if level != AlertError {
			t.Errorf("expected AlertError, got %s", level)
		}
	})
}
