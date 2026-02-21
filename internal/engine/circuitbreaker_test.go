package engine

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, time.Minute)

	if !cb.Allow() {
		t.Fatal("closed breaker should allow calls")
	}

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CBClosed {
		t.Fatal("should still be closed after 2 failures")
	}

	cb.RecordFailure()
	if cb.State() != CBOpen {
		t.Fatal("should be open after 3 failures")
	}

	if cb.Allow() {
		t.Fatal("open breaker should not allow calls")
	}
}

func TestCircuitBreaker_HalfOpenSuccess(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, 10*time.Millisecond)

	cb.RecordFailure()
	if cb.State() != CBOpen {
		t.Fatal("should be open")
	}

	time.Sleep(15 * time.Millisecond)

	if !cb.Allow() {
		t.Fatal("should transition to half-open after reset timeout")
	}
	if cb.State() != CBHalfOpen {
		t.Fatal("should be half-open")
	}

	cb.RecordSuccess()
	if cb.State() != CBClosed {
		t.Fatal("should be closed after successful probe")
	}
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, 10*time.Millisecond)

	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)

	cb.Allow() // transitions to half-open
	cb.RecordFailure()

	if cb.State() != CBOpen {
		t.Fatal("should be re-opened after probe failure")
	}
}

func TestCircuitBreaker_ResetOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, time.Minute)

	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess()

	if cb.State() != CBClosed {
		t.Fatal("success should reset to closed")
	}

	// Failures should start from 0 again.
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CBClosed {
		t.Fatal("should still be closed at 2 failures after reset")
	}
}

func TestCircuitBreaker_Execute(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, time.Minute)

	// Successful execution.
	err := cb.Execute(func() error { return nil })
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Failed execution opens breaker.
	err = cb.Execute(func() error { return errors.New("fail") })
	if err == nil || err.Error() != "fail" {
		t.Fatalf("expected 'fail' error, got %v", err)
	}

	// Breaker is now open.
	err = cb.Execute(func() error { return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCircuitBreaker_HalfOpenRejectsConcurrent(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, 10*time.Millisecond)

	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)

	// First Allow transitions to half-open.
	if !cb.Allow() {
		t.Fatal("first call should be allowed (half-open)")
	}

	// Second call while half-open should be rejected.
	if cb.Allow() {
		t.Fatal("concurrent call during half-open probe should be rejected")
	}
}
