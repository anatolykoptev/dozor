package a2a

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestWithRetry_success_first_attempt(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), RetryOpts{MaxAttempts: 3, InitialDelay: time.Millisecond}, "test", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_retries_on_network_error(t *testing.T) {
	calls := 0
	netErr := errors.New("connection refused")
	err := withRetry(context.Background(), RetryOpts{MaxAttempts: 3, InitialDelay: time.Millisecond}, "test", func() error {
		calls++
		if calls < 3 {
			return netErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_no_retry_on_4xx(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), RetryOpts{MaxAttempts: 3, InitialDelay: time.Millisecond}, "test", func() error {
		calls++
		return &httpError{status: http.StatusBadRequest, msg: "bad request"}
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry on 400), got %d", calls)
	}
}

func TestWithRetry_retries_on_429(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), RetryOpts{MaxAttempts: 3, InitialDelay: time.Millisecond}, "test", func() error {
		calls++
		if calls < 3 {
			return &httpError{status: http.StatusTooManyRequests, msg: "rate limited"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_exhausted(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), RetryOpts{MaxAttempts: 3, InitialDelay: time.Millisecond}, "test", func() error {
		calls++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error after exhaustion")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_context_cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := withRetry(ctx, RetryOpts{MaxAttempts: 5, InitialDelay: 50 * time.Millisecond}, "test", func() error {
		calls++
		if calls == 1 {
			cancel()
		}
		return errors.New("fail")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancel, got %d", calls)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"network", errors.New("dial tcp: refused"), true},
		{"400", &httpError{status: 400}, false},
		{"401", &httpError{status: 401}, false},
		{"429", &httpError{status: 429}, true},
		{"500", &httpError{status: 500}, true},
		{"503", &httpError{status: 503}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryable(tt.err); got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
