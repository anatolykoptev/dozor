package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProbeResult is the result of probing one URL.
type ProbeResult struct {
	URL             string
	Status          int
	LatencyMs       int64
	OK              bool
	SSLDays         *int
	SSLExpiry       *time.Time
	Error           string
	SecurityHeaders *SecurityHeadersResult
}

// SecurityHeadersResult from auditing HTTP response headers.
type SecurityHeadersResult struct {
	HSTS                string
	CSP                 string
	XFrameOptions       string
	XContentTypeOptions string
	ReferrerPolicy      string
	Missing             []string
}

// DNSResult from resolving a hostname.
type DNSResult struct {
	Hostname string
	A        []string
	AAAA     []string
	CNAME    string
	MX       []string
	Error    string
}

// ProbeURLs checks all URLs concurrently and returns results.
func ProbeURLs(ctx context.Context, urls []string, timeoutSec int, checkHeaders bool) []ProbeResult {
	if timeoutSec <= 0 {
		timeoutSec = 10
	}

	results := make([]ProbeResult, len(urls))
	var wg sync.WaitGroup

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, rawURL string) {
			defer wg.Done()
			results[idx] = probeOne(ctx, rawURL, timeoutSec, checkHeaders)
		}(i, u)
	}
	wg.Wait()
	return results
}

func probeOne(ctx context.Context, rawURL string, timeoutSec int, checkHeaders bool) ProbeResult {
	r := ProbeResult{URL: rawURL}

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

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
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

	if checkHeaders {
		r.SecurityHeaders = checkSecurityHeaders(resp)
	}

	return r
}

func checkSecurityHeaders(resp *http.Response) *SecurityHeadersResult {
	h := &SecurityHeadersResult{
		HSTS:                resp.Header.Get("Strict-Transport-Security"),
		CSP:                 resp.Header.Get("Content-Security-Policy"),
		XFrameOptions:       resp.Header.Get("X-Frame-Options"),
		XContentTypeOptions: resp.Header.Get("X-Content-Type-Options"),
		ReferrerPolicy:      resp.Header.Get("Referrer-Policy"),
	}
	if h.HSTS == "" {
		h.Missing = append(h.Missing, "Strict-Transport-Security")
	}
	if h.CSP == "" {
		h.Missing = append(h.Missing, "Content-Security-Policy")
	}
	if h.XFrameOptions == "" {
		h.Missing = append(h.Missing, "X-Frame-Options")
	}
	if h.XContentTypeOptions == "" {
		h.Missing = append(h.Missing, "X-Content-Type-Options")
	}
	if h.ReferrerPolicy == "" {
		h.Missing = append(h.Missing, "Referrer-Policy")
	}
	return h
}

// ProbeDNS resolves hostnames concurrently using Go's net.Resolver.
func ProbeDNS(ctx context.Context, hostnames []string) []DNSResult {
	results := make([]DNSResult, len(hostnames))
	var wg sync.WaitGroup
	resolver := &net.Resolver{}

	for i, h := range hostnames {
		wg.Add(1)
		go func(idx int, hostname string) {
			defer wg.Done()
			results[idx] = resolveDNS(ctx, resolver, hostname)
		}(i, h)
	}
	wg.Wait()
	return results
}

func resolveDNS(ctx context.Context, resolver *net.Resolver, hostname string) DNSResult {
	r := DNSResult{Hostname: hostname}

	// A records
	ips, err := resolver.LookupHost(ctx, hostname)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if parsed.To4() != nil {
			r.A = append(r.A, ip)
		} else {
			r.AAAA = append(r.AAAA, ip)
		}
	}

	// CNAME
	cname, err := resolver.LookupCNAME(ctx, hostname)
	if err == nil && cname != hostname && cname != hostname+"." {
		r.CNAME = strings.TrimSuffix(cname, ".")
	}

	// MX records
	mxRecords, err := resolver.LookupMX(ctx, hostname)
	if err == nil {
		for _, mx := range mxRecords {
			r.MX = append(r.MX, fmt.Sprintf("%s (priority %d)", strings.TrimSuffix(mx.Host, "."), mx.Pref))
		}
	}

	return r
}

// ExtractHostname strips scheme, port, and path from a URL.
func ExtractHostname(rawURL string) string {
	// Try parsing as URL first
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		host := parsed.Hostname() // strips port
		return host
	}
	// Strip port if present
	if idx := strings.LastIndex(rawURL, ":"); idx > 0 {
		return rawURL[:idx]
	}
	return rawURL
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

		if r.SecurityHeaders != nil {
			h := r.SecurityHeaders
			if len(h.Missing) > 0 {
				fmt.Fprintf(&b, "     Security headers [!!]: missing %s\n", strings.Join(h.Missing, ", "))
			} else {
				b.WriteString("     Security headers [OK]: all present\n")
			}
			if h.HSTS != "" {
				fmt.Fprintf(&b, "       HSTS: %s\n", h.HSTS)
			}
			if h.CSP != "" {
				csp := h.CSP
				if len(csp) > 80 {
					csp = csp[:80] + "..."
				}
				fmt.Fprintf(&b, "       CSP: %s\n", csp)
			}
		}

		b.WriteString("\n")
	}

	return b.String()
}

// FormatDNSResults formats DNS resolution results for display.
func FormatDNSResults(results []DNSResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "DNS Resolution (%d hostnames)\n\n", len(results))

	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(&b, "[!!] %s\n    Error: %s\n\n", r.Hostname, r.Error)
			continue
		}

		fmt.Fprintf(&b, "[OK] %s\n", r.Hostname)
		if len(r.A) > 0 {
			fmt.Fprintf(&b, "     A:     %s\n", strings.Join(r.A, ", "))
		}
		if len(r.AAAA) > 0 {
			fmt.Fprintf(&b, "     AAAA:  %s\n", strings.Join(r.AAAA, ", "))
		}
		if r.CNAME != "" {
			fmt.Fprintf(&b, "     CNAME: %s\n", r.CNAME)
		}
		if len(r.MX) > 0 {
			fmt.Fprintf(&b, "     MX:    %s\n", strings.Join(r.MX, ", "))
		}
		b.WriteString("\n")
	}

	return b.String()
}
