package a2a

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// maxErrBodyBytes is the maximum number of bytes included in error messages.
const maxErrBodyBytes = 200

// RetryOpts controls retry behaviour for A2A calls.
type RetryOpts struct {
	// MaxAttempts is the total number of attempts (1 = no retry).
	MaxAttempts int
	// InitialDelay is the wait before the second attempt.
	InitialDelay time.Duration
	// MaxDelay caps the exponential backoff growth.
	MaxDelay time.Duration
}

// DefaultRetryOpts is the recommended production configuration:
// up to 4 attempts with 1s → 2s → 4s backoff, capped at 8s.
var DefaultRetryOpts = RetryOpts{
	MaxAttempts:  4,
	InitialDelay: time.Second,
	MaxDelay:     8 * time.Second,
}

// isRetryable reports whether err warrants a retry.
// Network errors and HTTP 429/5xx are retryable; 4xx (except 429) are not.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.status == http.StatusTooManyRequests || he.status >= http.StatusInternalServerError
	}
	// Any non-HTTP error (network timeout, connection refused, etc.) is retryable.
	return true
}

// httpError carries an HTTP status code so isRetryable can inspect it.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// truncate shortens s to max bytes, appending "..." if truncated.
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// withRetry executes fn with exponential backoff according to opts.
// It stops early if ctx is cancelled or the error is not retryable.
func withRetry(ctx context.Context, opts RetryOpts, op string, fn func() error) error {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 1
	}
	delay := opts.InitialDelay

	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !isRetryable(lastErr) {
			return lastErr
		}
		if attempt == opts.MaxAttempts {
			break
		}

		slog.Warn("a2a: retrying",
			slog.String("op", op),
			slog.Int("attempt", attempt),
			slog.Duration("backoff", delay),
			slog.String("err", lastErr.Error()))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2
		if delay > opts.MaxDelay {
			delay = opts.MaxDelay
		}
	}
	return lastErr
}
