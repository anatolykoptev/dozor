package engine

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/anatolykoptev/go-kit/tracing/httpmw"
)

const (
	// maxHTTPRedirects is the maximum number of HTTP redirects to follow.
	maxHTTPRedirects = 5
)

// newHTTPClient creates an HTTP client with TLS verification, redirect limit,
// the given timeout, and OTel client-span instrumentation. Used by probe and
// remote health checks.
//
// The transport is wrapped with httpmw.WrapTransport so each outgoing call
// emits a span and injects traceparent — distributed traces survive the
// hop into remote services.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxHTTPRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
		Transport: httpmw.WrapTransport(&http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}),
	}
}
