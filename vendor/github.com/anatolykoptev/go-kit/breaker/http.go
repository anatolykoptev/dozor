// breaker/http.go
package breaker

import (
	"fmt"
	"net/http"
)

// httpDoer is the minimal HTTP client interface (implemented by *http.Client).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPDoer wraps an httpDoer with a Breaker. Responses with status >= 500 count
// as failures; transport errors count as failures; 2xx-4xx responses count as
// successes. Returns ErrOpen when the breaker is open.
//
// Body handling: on 5xx, the response body is closed and a sentinel error is
// returned — callers never see the 5xx response object, by design (if you need
// the body, fetch without the breaker wrap).
type HTTPDoer struct {
	inner httpDoer
	b     *Breaker
}

func NewHTTPDoer(inner httpDoer, b *Breaker) *HTTPDoer {
	return &HTTPDoer{inner: inner, b: b}
}

func (d *HTTPDoer) Do(req *http.Request) (*http.Response, error) {
	return Execute(d.b, func() (*http.Response, error) {
		resp, err := d.inner.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s: HTTP %d", d.b.opts.Name, resp.StatusCode)
		}
		return resp, nil
	})
}
