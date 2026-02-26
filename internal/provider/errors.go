package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// httpStatusRateLimit is the HTTP status code for rate limiting.
	httpStatusRateLimit = http.StatusTooManyRequests
	// errorBodyTruncLen is the maximum length of an error body message to include.
	errorBodyTruncLen = 300
	// httpServerErrorMin is the minimum HTTP status for server errors.
	httpServerErrorMinProvider = 500
	// httpServerErrorMax is the maximum HTTP status for server errors.
	httpServerErrorMax = 600
)

// ProviderError is a structured error from an LLM provider.
type ProviderError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
	Raw        string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("LLM API error %d: %s", e.StatusCode, e.Message)
}

// IsAuth returns true for 401/403 authentication errors.
func (e *ProviderError) IsAuth() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// IsRateLimit returns true for 429 quota/rate-limit errors.
func (e *ProviderError) IsRateLimit() bool {
	return e.StatusCode == httpStatusRateLimit
}

// IsServerError returns true for 5xx server errors.
func (e *ProviderError) IsServerError() bool {
	return e.StatusCode >= httpServerErrorMinProvider && e.StatusCode < httpServerErrorMax
}

// IsTransient returns true if the error is worth retrying.
func (e *ProviderError) IsTransient() bool {
	return e.IsRateLimit() || e.IsServerError()
}

// parseProviderError parses a non-200 HTTP response body into a ProviderError.
func parseProviderError(statusCode int, body []byte) *ProviderError {
	pe := &ProviderError{
		StatusCode: statusCode,
		Raw:        string(body),
	}

	// Google/Gemini format with details array (includes retry delay).
	var googleErr struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &googleErr) == nil && googleErr.Error.Message != "" {
		pe.Message = googleErr.Error.Message
		for _, d := range googleErr.Error.Details {
			if delay, ok := d.Metadata["retryDelay"]; ok {
				pe.RetryAfter = parseRetryDelay(delay)
			}
		}
		return pe
	}

	// OpenAI-compat format: {"error": {"message": "..."}}
	var openaiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &openaiErr) == nil && openaiErr.Error.Message != "" {
		pe.Message = openaiErr.Error.Message
		return pe
	}

	// Fallback: first line of body
	s := strings.TrimSpace(string(body))
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	if len(s) > errorBodyTruncLen {
		s = s[:errorBodyTruncLen] + "..."
	}
	pe.Message = s
	return pe
}

// parseRetryDelay parses strings like "30s", "2m", "5m30s".
func parseRetryDelay(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 0
}
