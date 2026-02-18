package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderError is a structured error from an LLM provider.
type ProviderError struct {
	StatusCode int
	Message    string
	Raw        string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("LLM API error %d: %s", e.StatusCode, e.Message)
}

// IsAuth returns true for 401/403 authentication errors.
func (e *ProviderError) IsAuth() bool {
	return e.StatusCode == 401 || e.StatusCode == 403
}

// IsRateLimit returns true for 429 quota/rate-limit errors.
func (e *ProviderError) IsRateLimit() bool {
	return e.StatusCode == 429
}

// IsServerError returns true for 5xx server errors.
func (e *ProviderError) IsServerError() bool {
	return e.StatusCode >= 500 && e.StatusCode < 600
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

	// OpenAI / Gemini OpenAI-compat format: {"error": {"message": "..."}}
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		pe.Message = errResp.Error.Message
		return pe
	}

	// Fallback: first line of body
	s := strings.TrimSpace(string(body))
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	pe.Message = s
	return pe
}
