package telegram

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// isTransientTelegramError
// ---------------------------------------------------------------------------

func TestIsTransientTelegramError(t *testing.T) {
	transientCases := []struct {
		name string
		msg  string
	}{
		{"rate limit 429", "telegram: error code 429"},
		{"bad gateway 502", "502 Bad Gateway"},
		{"service unavailable 503", "503 Service Unavailable"},
		{"gateway timeout 504", "504 Gateway Timeout"},
		{"timeout keyword", "request timeout exceeded"},
		{"connection reset", "connection reset by peer"},
		{"connection refused", "connection refused"},
	}
	for _, tc := range transientCases {
		t.Run(tc.name, func(t *testing.T) {
			err := errors.New(tc.msg)
			if !isTransientTelegramError(err) {
				t.Errorf("isTransientTelegramError(%q) = false, want true", tc.msg)
			}
		})
	}

	nonTransientCases := []struct {
		name string
		msg  string
	}{
		{"forbidden 403", "telegram: error code 403"},
		{"bad request 400", "400 Bad Request"},
		{"not found 404", "404 Not Found"},
		{"parse error", "can't parse entities"},
		{"empty error", ""},
	}
	for _, tc := range nonTransientCases {
		t.Run(tc.name, func(t *testing.T) {
			err := errors.New(tc.msg)
			if isTransientTelegramError(err) {
				t.Errorf("isTransientTelegramError(%q) = true, want false", tc.msg)
			}
		})
	}
}
