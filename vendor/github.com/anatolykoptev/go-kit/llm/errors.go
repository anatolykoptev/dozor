package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// APIError is a structured error returned by LLM API calls.
// Use errors.As to extract status code, body, and error type from callers.
type APIError struct {
	StatusCode int
	Body       string
	Type       string // parsed from JSON response if available (e.g. "rate_limit_error")
	Code       string // parsed from JSON response if available (e.g. "context_length_exceeded")
	Retryable  bool
	// RetryAfter is the server-suggested delay before retry, parsed from
	// the HTTP Retry-After response header (RFC 7231 §7.1.3). Zero when
	// the header is absent or unparseable. Callers retrying APIError
	// should honour this value when non-zero instead of their own
	// backoff schedule.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("llm: HTTP %d (%s): %s", e.StatusCode, e.Type, e.Body)
	}
	return fmt.Sprintf("llm: HTTP %d: %s", e.StatusCode, e.Body)
}

func newAPIError(statusCode int, body string, retryable bool, retryAfter time.Duration) *APIError {
	e := &APIError{
		StatusCode: statusCode,
		Body:       body,
		Retryable:  retryable,
		RetryAfter: retryAfter,
	}
	// Try to extract error type/code from JSON body (OpenAI/Anthropic format).
	var parsed struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil {
		e.Type = parsed.Error.Type
		e.Code = parsed.Error.Code
	}
	return e
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func asRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

// asFailover reports whether err is a per-model "request too large" failure:
// the request exceeds THIS model's context window or per-minute token budget.
//
// Such an error is NOT retryable on the same endpoint — the identical request
// recurs and would 413/400 again — but the NEXT model in a fallback chain may
// have a larger context window or a higher token budget. So the endpoint loop
// should ADVANCE to it rather than abort the whole chain.
//
// Recognised signals (cross-provider, observed on the cliproxyapi fleet):
//   - HTTP 413 Payload Too Large — Groq emits this with type "tokens" when a
//     single request exceeds the model's per-minute token (TPM) budget.
//   - HTTP 400 with error code "context_length_exceeded" — the OpenAI-family
//     context-window-overflow shape.
//
// A plain 400 (malformed request) is deliberately NOT a failover: it recurs
// identically on every model, so the chain must abort, not burn every endpoint.
//
// Note: 413 is matched on status alone (any body) — treated as model-specific.
// A non-model 413 shared by every endpoint (e.g. a gateway payload-size limit)
// would still advance and burn the chain, surfacing the same error as before
// just after N attempts. Acceptable for the same-proxy chains this targets.
func asFailover(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusRequestEntityTooLarge { // 413
		return true
	}
	if apiErr.StatusCode == http.StatusBadRequest && apiErr.Code == "context_length_exceeded" {
		return true
	}
	return false
}

// parseRetryAfter parses the HTTP Retry-After header per RFC 7231. The
// value can be either a non-negative integer number of seconds or an
// HTTP-date. Returns 0 on empty or unparseable input.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	// Seconds form.
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form.
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
