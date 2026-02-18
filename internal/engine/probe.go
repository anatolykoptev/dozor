package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ProbeResult is the result of probing one URL.
type ProbeResult struct {
	URL        string
	Status     int
	LatencyMs  int64
	OK         bool
	SSLDays    *int
	SSLExpiry  *time.Time
	Error      string
}

// ProbeURLs checks all URLs concurrently and returns results.
func ProbeURLs(ctx context.Context, urls []string, timeoutSec int) []ProbeResult {
	if timeoutSec <= 0 {
		timeoutSec = 10
	}

	results := make([]ProbeResult, len(urls))
	var wg sync.WaitGroup

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			results[idx] = probeOne(ctx, url, timeoutSec)
		}(i, u)
	}
	wg.Wait()
	return results
}

func probeOne(ctx context.Context, url string, timeoutSec int) ProbeResult {
	r := ProbeResult{URL: url}

	client := &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
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

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}

	start := time.Now()
	resp, err := client.Do(req)
	r.LatencyMs = time.Since(start).Milliseconds()

	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer resp.Body.Close()

	r.Status = resp.StatusCode
	r.OK = resp.StatusCode >= 200 && resp.StatusCode < 400

	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		expiry := resp.TLS.PeerCertificates[0].NotAfter
		r.SSLExpiry = &expiry
		days := int(time.Until(expiry).Hours() / 24)
		r.SSLDays = &days
	}

	return r
}

// FormatProbeResults formats probe results for display.
func FormatProbeResults(results []ProbeResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP Probe Results (%d URLs)\n\n", len(results))

	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(&b, "[!!] %s\n    Error: %s\n\n", r.URL, r.Error)
			continue
		}

		icon := "OK"
		if !r.OK {
			icon = "!!"
		}

		fmt.Fprintf(&b, "[%s] %s — HTTP %d (%dms)\n", icon, r.URL, r.Status, r.LatencyMs)

		if r.SSLDays != nil {
			sslIcon := "OK"
			if *r.SSLDays < 7 {
				sslIcon = "CRITICAL"
			} else if *r.SSLDays < 30 {
				sslIcon = "WARNING"
			}
			fmt.Fprintf(&b, "     SSL [%s]: %d days remaining (expires %s)\n",
				sslIcon, *r.SSLDays, r.SSLExpiry.Format("2006-01-02"))
		}
		b.WriteString("\n")
	}

	return b.String()
}
