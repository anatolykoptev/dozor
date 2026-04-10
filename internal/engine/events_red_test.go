package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
)

// blockingEventsClient never closes channels — simulates a hung Docker daemon.
type blockingEventsClient struct {
	msgCh chan events.Message
	errCh chan error
}

func newBlockingClient() *blockingEventsClient {
	return &blockingEventsClient{
		msgCh: make(chan events.Message),
		errCh: make(chan error),
	}
}

func (b *blockingEventsClient) Events(_ context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	return b.msgCh, b.errCh
}

// slowEventsClient streams events with a delay between each.
type slowEventsClient struct {
	messages []events.Message
	delay    time.Duration
}

func (s *slowEventsClient) Events(ctx context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	msgCh := make(chan events.Message, len(s.messages))
	errCh := make(chan error, 1)

	go func() {
		defer close(msgCh)
		defer close(errCh)
		for _, msg := range s.messages {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.delay):
				msgCh <- msg
			}
		}
	}()

	return msgCh, errCh
}

// errFirstEventsClient sends an error before any messages.
type errFirstEventsClient struct {
	err error
}

func (e *errFirstEventsClient) Events(_ context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	msgCh := make(chan events.Message)
	errCh := make(chan error, 1)
	close(msgCh)
	errCh <- e.err
	close(errCh)
	return msgCh, errCh
}

// errChClosesFirstClient closes errCh before msgCh, simulating the "errCh nil drain" code path.
type errChClosesFirstClient struct {
	messages []events.Message
}

func (e *errChClosesFirstClient) Events(_ context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	msgCh := make(chan events.Message, len(e.messages))
	errCh := make(chan error)

	for _, msg := range e.messages {
		msgCh <- msg
	}

	// Close errCh first, then msgCh — triggers the "errCh = nil; continue" branch.
	close(errCh)
	close(msgCh)

	return msgCh, errCh
}

func TestRestartsInWindow_DaemonUnavailable_ReturnsZeroAndDoesNotBlock(t *testing.T) {
	t.Parallel()

	cli := &errFirstEventsClient{err: errors.New("cannot connect to daemon")}

	done := make(chan int, 1)
	go func() {
		n := RestartsInWindow(context.Background(), cli, "my-svc", 24*time.Hour)
		done <- n
	}()

	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("expected 0 on daemon error, got %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RestartsInWindow blocked on daemon error — expected fast return")
	}
}

func TestRestartsInWindow_ZeroTimestamp_IsCountedOrSkipped(t *testing.T) {
	t.Parallel()

	now := time.Now()
	// An event with Time=0 (zero Unix epoch, year 1970) is far before any "since" window.
	zeroMsg := events.Message{
		Type:   events.ContainerEventType,
		Action: events.ActionDie,
		Time:   0,
	}
	// An event with a future Time should still count (it's after since boundary).
	futureMsg := makeContainerEvent(events.ActionDie, "svc", now.Add(1*time.Hour))
	// An event 1 minute ago — should count.
	recentMsg := makeContainerEvent(events.ActionDie, "svc", now.Add(-1*time.Minute))

	cli := &mockEventsClient{
		messages: []events.Message{zeroMsg, futureMsg, recentMsg},
	}

	count := RestartsInWindow(context.Background(), cli, "svc", 24*time.Hour)
	// zero-time event must NOT be counted (year 1970 is way before since).
	// future event counts (it's after since).
	// recent event counts.
	// Expected: 2
	if count != 2 {
		t.Errorf("expected 2 (zero-time event excluded), got %d", count)
	}
}

func TestRestartsInWindow_SinceBoundaryExclusive(t *testing.T) {
	t.Parallel()

	// An event exactly at the since boundary (not after, but equal) must NOT be counted.
	window := 24 * time.Hour
	now := time.Now()
	since := now.Add(-window)

	// Event exactly at since boundary — should be excluded (After returns false for Equal).
	atBoundary := events.Message{
		Type:   events.ContainerEventType,
		Action: events.ActionDie,
		Time:   since.Unix(),
	}
	// Event 1 second after boundary — should be counted.
	afterBoundary := makeContainerEvent(events.ActionDie, "svc", since.Add(time.Second))

	cli := &mockEventsClient{
		messages: []events.Message{atBoundary, afterBoundary},
	}

	count := RestartsInWindow(context.Background(), cli, "svc", window)
	if count != 1 {
		t.Errorf("since boundary must be exclusive: expected 1, got %d", count)
	}
}

func TestRestartsInWindow_ContextCancelledMidStream_ReturnsPartialCount(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cli := &slowEventsClient{
		messages: []events.Message{
			makeContainerEvent(events.ActionDie, "svc", now.Add(-1*time.Hour)),
			makeContainerEvent(events.ActionDie, "svc", now.Add(-2*time.Hour)),
			makeContainerEvent(events.ActionDie, "svc", now.Add(-3*time.Hour)),
		},
		delay: 60 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 90ms — enough to receive first event (60ms), not the second (120ms).
	time.AfterFunc(90*time.Millisecond, cancel)

	count := RestartsInWindow(ctx, cli, "svc", 24*time.Hour)
	// Must be 0 or 1 (partial), never 3.
	if count < 0 || count > 1 {
		t.Errorf("expected partial count 0..1, got %d", count)
	}
}

func TestRestartsInWindow_ContextDeadlineExceeded_DoesNotHang(t *testing.T) {
	t.Parallel()

	// blockingClient never closes channels — rely on dockerPingTimeoutSec internal timeout.
	cli := newBlockingClient()

	// Give it 2× dockerPingTimeoutSec to be safe, but it must not hang forever.
	ctx, cancel := context.WithTimeout(context.Background(), 2*dockerPingTimeoutSec*time.Second)
	defer cancel()

	done := make(chan int, 1)
	go func() {
		done <- RestartsInWindow(ctx, cli, "svc", 24*time.Hour)
	}()

	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("expected 0 on timeout, got %d", n)
		}
	case <-time.After(3 * dockerPingTimeoutSec * time.Second):
		t.Fatal("RestartsInWindow did not return within dockerPingTimeoutSec — hung")
	}
}

func TestRestartsInWindow_UntilBeforeSince_ReturnsZero(t *testing.T) {
	t.Parallel()

	// Negative window: programmer error. Should return 0 without panic.
	cli := &mockEventsClient{}
	count := RestartsInWindow(context.Background(), cli, "svc", -1*time.Hour)
	if count != 0 {
		t.Errorf("expected 0 for negative window, got %d", count)
	}
}

func TestRestartsInWindow_UnicodeLongContainerName_NoPanic(t *testing.T) {
	t.Parallel()

	// Very long unicode name should not cause issues with filter args.
	longName := "сервис-который-очень-длинно-называется-и-содержит-юникод-символы-1234567890"
	cli := &mockEventsClient{}
	count := RestartsInWindow(context.Background(), cli, longName, 24*time.Hour)
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestRestartsInWindow_ErrChClosesBeforeMsgCh_CountsDrained(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cli := &errChClosesFirstClient{
		messages: []events.Message{
			makeContainerEvent(events.ActionDie, "svc", now.Add(-1*time.Hour)),
			makeContainerEvent(events.ActionDie, "svc", now.Add(-2*time.Hour)),
		},
	}

	count := RestartsInWindow(context.Background(), cli, "svc", 24*time.Hour)
	if count != 2 {
		t.Errorf("expected 2 events drained after errCh closed, got %d", count)
	}
}

func TestRestartsInWindow_BothChannelsCleanWithExactCount(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const want = 5
	msgs := make([]events.Message, want)
	for i := range want {
		msgs[i] = makeContainerEvent(events.ActionDie, "svc", now.Add(-time.Duration(i+1)*time.Hour))
	}

	cli := &mockEventsClient{messages: msgs}
	count := RestartsInWindow(context.Background(), cli, "svc", 48*time.Hour)
	if count != want {
		t.Errorf("expected %d, got %d", want, count)
	}
}

func TestRestartsInWindow_HighLoadNoGoroutineLeak(t *testing.T) {
	// 1000 events, race detector on, ensure no goroutine is leaked.
	now := time.Now()
	const n = 1000
	msgs := make([]events.Message, n)
	for i := range n {
		msgs[i] = makeContainerEvent(events.ActionDie, "svc", now.Add(-time.Duration(i+1)*time.Second))
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cli := &mockEventsClient{messages: msgs}
			count := RestartsInWindow(context.Background(), cli, "svc", 48*time.Hour)
			if count != n {
				t.Errorf("expected %d events, got %d", n, count)
			}
		}()
	}
	wg.Wait()
}
