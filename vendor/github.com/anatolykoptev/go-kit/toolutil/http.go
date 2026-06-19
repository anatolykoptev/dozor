package toolutil

import (
	"encoding/json"
	"fmt"
)

// CheckHTTPStatus returns an error string if HTTP status >= 400, or empty on success.
// If the body is a JSON object with a "message" field, it is used for a readable error.
func CheckHTTPStatus(body []byte, status int) string {
	if status < 400 { //nolint:mnd
		return ""
	}
	if msg := jsonMessage(body); msg != "" {
		return fmt.Sprintf("HTTP %d: %s", status, msg)
	}
	const truncLen = 500
	return fmt.Sprintf("HTTP %d: %s", status, TruncateStr(string(body), truncLen))
}

// jsonMessage extracts "code" (or "message") from a JSON error body.
// Prefers "code" because it is always English (e.g. "rest_post_invalid_id"),
// while "message" follows the server locale and may contain non-ASCII escapes.
func jsonMessage(body []byte) string {
	var obj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return ""
	}
	if obj.Code != "" {
		return obj.Code
	}
	return obj.Message
}

// SafeDate truncates an ISO date string to YYYY-MM-DD, or returns "unknown" for empty.
func SafeDate(d string) string {
	if len(d) >= 10 { //nolint:mnd
		return d[:10]
	}
	if d == "" {
		return "unknown"
	}
	return d
}
