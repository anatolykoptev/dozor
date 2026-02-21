package engine

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"
)

// newHTTPClient creates an HTTP client with TLS verification, redirect limit,
// and the given timeout. Used by probe and remote health checks.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		},
	}
}
