package provider

import (
	"errors"
	"fmt"
	"net"
	"testing"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// Golden table — every classification scenario the retry loop and
// fallback decision care about. Maintainable as new status codes appear.
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		isAuth      bool
		isRateLimit bool
		isServer    bool
		isTransient bool
	}{
		{"401 unauthorized", &kitllm.APIError{StatusCode: 401, Body: `{"error":"bad key"}`}, true, false, false, false},
		{"403 forbidden", &kitllm.APIError{StatusCode: 403}, true, false, false, false},
		{"429 rate limit", &kitllm.APIError{StatusCode: 429}, false, true, false, true},
		{"500 server", &kitllm.APIError{StatusCode: 500}, false, false, true, true},
		{"502 bad gateway", &kitllm.APIError{StatusCode: 502}, false, false, true, true},
		{"503 unavailable", &kitllm.APIError{StatusCode: 503}, false, false, true, true},
		{"504 timeout", &kitllm.APIError{StatusCode: 504}, false, false, true, true},
		{"400 bad request", &kitllm.APIError{StatusCode: 400}, false, false, false, false},
		{"404 not found", &kitllm.APIError{StatusCode: 404}, false, false, false, false},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("network unreachable")}, false, false, false, true},
		{"generic error", fmt.Errorf("totally unrelated"), false, false, false, false},
		{"nil", nil, false, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsAuth(c.err); got != c.isAuth {
				t.Errorf("IsAuth: got %v, want %v", got, c.isAuth)
			}
			if got := IsRateLimit(c.err); got != c.isRateLimit {
				t.Errorf("IsRateLimit: got %v, want %v", got, c.isRateLimit)
			}
			if got := IsServerError(c.err); got != c.isServer {
				t.Errorf("IsServerError: got %v, want %v", got, c.isServer)
			}
			if got := IsTransient(c.err); got != c.isTransient {
				t.Errorf("IsTransient: got %v, want %v", got, c.isTransient)
			}
		})
	}
}

// TestErrorClass_Ordering locks the switch ordering: sentinel and specific
// classes must win over the generic http_NNN arm (401→auth not http_401,
// 429→rate_limit not http_429).
func TestErrorClass_Ordering(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"unavailable", ErrUnavailable, "unavailable"},
		{"auth_401", &kitllm.APIError{StatusCode: 401}, "auth"},
		{"auth_403", &kitllm.APIError{StatusCode: 403}, "auth"},
		{"rate_limit_429", &kitllm.APIError{StatusCode: 429}, "rate_limit"},
		{"http_500", &kitllm.APIError{StatusCode: 500}, "http_500"},
		{"http_413", &kitllm.APIError{StatusCode: 413}, "http_413"},
		{"network", &net.OpError{Op: "dial", Err: errors.New("refused")}, "network"},
		{"other", errors.New("boom"), "other"},
	}
	for _, tc := range cases {
		if got := ErrorClass(tc.err); got != tc.want {
			t.Errorf("%s: ErrorClass = %q, want %q", tc.name, got, tc.want)
		}
	}
}
