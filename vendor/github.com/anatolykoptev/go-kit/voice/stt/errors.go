package stt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ErrResponseTooLarge means the server's response exceeded the size cap for
// the requested format; the body is discarded.
var ErrResponseTooLarge = errors.New("stt: response too large")

// ErrUnsupportedFormat is returned by [Client.Transcribe] for response formats
// whose body is not a transcript object (srt, vtt); use [Client.TranscribeRaw].
var ErrUnsupportedFormat = errors.New("stt: response format not supported by Transcribe; use TranscribeRaw")

// Error represents an STT API error with HTTP status code.
//
// It never carries the server's response body: the body can echo request
// content, so only the status and, at most, a strict machine token are kept.
type Error struct {
	StatusCode int
	// Message is the HTTP status text (or a fixed client-side description).
	Message string
	// Reason is the server's error code/type when it is a strict token such as
	// "invalid_api_key"; empty otherwise.
	Reason string
}

func (e *Error) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("stt: HTTP %d: %s (%s)", e.StatusCode, e.Message, e.Reason)
	}
	return fmt.Sprintf("stt: HTTP %d: %s", e.StatusCode, e.Message)
}

// IsTransient returns true for errors that may succeed on retry (429, 502-504).
func (e *Error) IsTransient() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// reasonToken matches the machine tokens servers put in error.code/error.type
// (e.g. "invalid_api_key", "rate_limit_exceeded").
//
// Deliberately narrow: all-lower or all-upper snake_case only, so key-shaped
// strings such as "sk-..." or "gsk_Ab12..." (mixed case, dashes) never qualify.
var reasonToken = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$|^[A-Z][A-Z0-9_]{1,63}$`)

// newHTTPError builds an *Error from a non-2xx status and (a bounded prefix of)
// its body. Only error.code or error.type are read, and only if they are a
// strict token; the message and the rest of the body are dropped.
//
// apiKey is the client's key: a reason containing it is dropped.
func newHTTPError(status int, body []byte, apiKey string) *Error {
	reason := errorReason(body)
	if apiKey != "" && strings.Contains(reason, apiKey) {
		reason = ""
	}
	return &Error{StatusCode: status, Message: http.StatusText(status), Reason: reason}
}

// stripURLError returns the cause of a *url.Error. net/http and net/url put the
// full request URL in that error's text; the base URL is not secret, but a URL
// can carry credentials, so no returned error wraps it.
func stripURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func errorReason(body []byte) string {
	var e struct {
		Error struct {
			Code json.RawMessage `json:"code"`
			Type string          `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	var code string // a numeric code fails to unmarshal and is ignored
	if json.Unmarshal(e.Error.Code, &code) == nil && reasonToken.MatchString(code) {
		return code
	}
	if reasonToken.MatchString(e.Error.Type) {
		return e.Error.Type
	}
	return ""
}

// Response size caps. Bodies are read through io.LimitReader so a misbehaving
// or hostile server cannot make the client buffer unbounded data.
const (
	maxTextResponse    = 4 << 20  // json, text: a transcript object / plain text
	maxVerboseResponse = 32 << 20 // verbose_json, srt, vtt: segments/words/cues for long audio
	maxModelsResponse  = 1 << 20
	maxErrorBody       = 64 << 10 // read only to find a reason token
)

// responseLimit returns the size cap for a response format.
func responseLimit(format string) int64 {
	switch format {
	case "verbose_json", "srt", "vtt":
		return maxVerboseResponse
	default:
		return maxTextResponse
	}
}

// readLimited reads r up to limit bytes and returns ErrResponseTooLarge if
// more is available.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, ErrResponseTooLarge
	}
	return b, nil
}
