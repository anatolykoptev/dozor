package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const webshareVendor = "webshare"

const webshareBaseURL = "https://proxy.webshare.io"

// WebshareProber probes the Webshare proxy API for bandwidth quota usage.
type WebshareProber struct {
	apiKey  string
	client  *http.Client
	baseURL string // overridable in tests
}

// NewWebshare returns a WebshareProber. Returns nil if apiKey is empty.
func NewWebshare(apiKey string) *WebshareProber {
	if apiKey == "" {
		return nil
	}
	return &WebshareProber{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: ProbeTimeout},
		baseURL: webshareBaseURL,
	}
}

func (w *WebshareProber) Vendor() string { return webshareVendor }

type webshareSubscription struct {
	BandwidthLimit int64 `json:"bandwidth"`
	BandwidthUsed  int64 `json:"bandwidth_used"`
	BandwidthGB    struct {
		Allowed float64 `json:"allowed"`
		Used    float64 `json:"used"`
	} `json:"bandwidth_gb"`
}

func (w *WebshareProber) Probe(ctx context.Context) ([]Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		w.baseURL+"/api/v2/subscription/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+w.apiKey)

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, &timeoutOrNetErr{err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusErr{status: resp.StatusCode}
	}

	var sub webshareSubscription
	if err := json.NewDecoder(resp.Body).Decode(&sub); err != nil {
		return nil, &parseErr{err}
	}

	var pct float64
	switch {
	case sub.BandwidthGB.Allowed > 0:
		remaining := sub.BandwidthGB.Allowed - sub.BandwidthGB.Used
		pct = remaining / sub.BandwidthGB.Allowed * 100
	case sub.BandwidthLimit > 0:
		remaining := float64(sub.BandwidthLimit - sub.BandwidthUsed)
		pct = remaining / float64(sub.BandwidthLimit) * 100
	default:
		return nil, &parseErr{errors.New("webshare: bandwidth limit is 0 or missing")}
	}

	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	return []Reading{{Product: "bandwidth", Remaining: pct}}, nil
}

// --- sentinel error types used by runner to classify failures ---

type timeoutOrNetErr struct{ err error }

func (e *timeoutOrNetErr) Error() string { return e.err.Error() }
func (e *timeoutOrNetErr) Unwrap() error { return e.err }

type httpStatusErr struct{ status int }

func (e *httpStatusErr) Error() string { return fmt.Sprintf("HTTP %d", e.status) }

type parseErr struct{ err error }

func (e *parseErr) Error() string { return e.err.Error() }
func (e *parseErr) Unwrap() error { return e.err }

// IsTimeout reports whether err is a network/timeout error.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*timeoutOrNetErr) //nolint:errorlint // direct type check
	return ok
}

// IsAuthFail reports whether err is a 401/403 error.
func IsAuthFail(err error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*httpStatusErr) //nolint:errorlint
	return ok && (e.status == http.StatusUnauthorized || e.status == http.StatusForbidden)
}

// IsHTTPErr reports whether err is a non-auth HTTP error.
func IsHTTPErr(err error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*httpStatusErr) //nolint:errorlint
	return ok && e.status != http.StatusUnauthorized && e.status != http.StatusForbidden
}

// IsParseErr reports whether err is a parse error.
func IsParseErr(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*parseErr) //nolint:errorlint
	return ok
}

const (
	// reasonTimeout is the Prometheus label for network/timeout failures.
	reasonTimeout = "timeout"
	// reasonAuthFail is the Prometheus label for 401/403 failures.
	reasonAuthFail = "auth_fail"
	// reasonParseErr is the Prometheus label for JSON parse failures.
	reasonParseErr = "parse_err"
)

// FailureReason maps a probe error to a Prometheus label value.
func FailureReason(err error) string {
	switch {
	case IsTimeout(err):
		return reasonTimeout
	case IsAuthFail(err):
		return reasonAuthFail
	case IsParseErr(err):
		return reasonParseErr
	default:
		return reasonHTTPErr
	}
}
