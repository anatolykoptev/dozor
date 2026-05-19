package provider

import (
	"errors"
	"net"
	"net/url"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// IsAuth reports whether err represents an authentication failure (HTTP
// 401 or 403). The fallback decision uses this to skip the secondary
// provider — if the primary's key is bad, the fallback's likely is too
// (typical setup shares the upstream provider).
func IsAuth(err error) bool {
	var ae *kitllm.APIError
	return errors.As(err, &ae) && (ae.StatusCode == 401 || ae.StatusCode == 403)
}

// IsRateLimit reports HTTP 429.
func IsRateLimit(err error) bool {
	var ae *kitllm.APIError
	return errors.As(err, &ae) && ae.StatusCode == 429
}

// IsServerError reports HTTP 5xx.
func IsServerError(err error) bool {
	var ae *kitllm.APIError
	return errors.As(err, &ae) && ae.StatusCode >= 500
}

// IsTransient reports whether err is worth retrying: rate-limit, server
// error, or a network-level failure. Auth (401/403) and client errors
// (400, 404, etc.) are not transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if IsRateLimit(err) || IsServerError(err) {
		return true
	}
	return isNetworkErr(err)
}

func isNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	return false
}
