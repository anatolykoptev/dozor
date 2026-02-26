package engine

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"
)

const (
	// maxHTTPRedirects is the maximum number of HTTP redirects to follow.
	maxHTTPRedirects = 5
)

// newHTTPClient creates an HTTP client with TLS verification, redirect limit,
// and the given timeout. Used by probe and remote health checks.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxHTTPRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}
