package engine

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExtractHostname(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"https://example.com/path", "example.com"},
		{"https://example.com:8443/path", "example.com"},
		{"http://example.com", "example.com"},
		{"example.com", "example.com"},
		{"example.com:443", "example.com"},
		{"https://sub.example.com/foo/bar?q=1", "sub.example.com"},
		{"localhost", "localhost"},
		{"localhost:8080", "localhost"},
		{"https://[::1]:443/path", "::1"},
	}
	for _, c := range cases {
		got := ExtractHostname(c.input)
		if got != c.expected {
			t.Errorf("ExtractHostname(%q) = %q, want %q", c.input, got, c.expected)
		}
	}
}

func TestCheckSecurityHeaders(t *testing.T) {
	t.Run("all present", func(t *testing.T) {
		resp := &http.Response{
			Header: http.Header{
				"Strict-Transport-Security": {"max-age=31536000"},
				"Content-Security-Policy":   {"default-src 'self'"},
				"X-Frame-Options":           {"DENY"},
				"X-Content-Type-Options":    {"nosniff"},
				"Referrer-Policy":           {"strict-origin"},
			},
		}
		result := checkSecurityHeaders(resp)
		if len(result.Missing) != 0 {
			t.Errorf("expected no missing headers, got: %v", result.Missing)
		}
		if result.HSTS != "max-age=31536000" {
			t.Errorf("HSTS = %q", result.HSTS)
		}
		if result.CSP != "default-src 'self'" {
			t.Errorf("CSP = %q", result.CSP)
		}
	})

	t.Run("all missing", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		result := checkSecurityHeaders(resp)
		if len(result.Missing) != 5 {
			t.Errorf("expected 5 missing headers, got %d: %v", len(result.Missing), result.Missing)
		}
	})

	t.Run("partial", func(t *testing.T) {
		resp := &http.Response{
			Header: http.Header{
				"Strict-Transport-Security": {"max-age=31536000"},
				"X-Content-Type-Options":    {"nosniff"},
			},
		}
		result := checkSecurityHeaders(resp)
		if len(result.Missing) != 3 {
			t.Errorf("expected 3 missing, got %d: %v", len(result.Missing), result.Missing)
		}
		// Should report CSP, X-Frame-Options, Referrer-Policy as missing
		for _, expected := range []string{"Content-Security-Policy", "X-Frame-Options", "Referrer-Policy"} {
			found := false
			for _, m := range result.Missing {
				if m == expected {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %q in missing list", expected)
			}
		}
	})
}

func TestFormatProbeResults(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		days := 90
		expiry := time.Now().Add(90 * 24 * time.Hour)
		results := []ProbeResult{
			{URL: "https://example.com", Status: 200, LatencyMs: 150, OK: true, SSLDays: &days, SSLExpiry: &expiry},
		}
		output := FormatProbeResults(results)
		if !strings.Contains(output, "[OK]") {
			t.Error("expected [OK] in output")
		}
		if !strings.Contains(output, "HTTP 200") {
			t.Error("expected HTTP 200")
		}
		if !strings.Contains(output, "150ms") {
			t.Error("expected latency")
		}
	})

	t.Run("with error", func(t *testing.T) {
		results := []ProbeResult{
			{URL: "https://down.com", Error: "connection refused"},
		}
		output := FormatProbeResults(results)
		if !strings.Contains(output, "[!!]") {
			t.Error("expected [!!] for error")
		}
		if !strings.Contains(output, "connection refused") {
			t.Error("expected error message")
		}
	})

	t.Run("with security headers", func(t *testing.T) {
		results := []ProbeResult{
			{
				URL: "https://example.com", Status: 200, OK: true, LatencyMs: 100,
				SecurityHeaders: &SecurityHeadersResult{
					HSTS:    "max-age=31536000",
					Missing: []string{"Content-Security-Policy", "X-Frame-Options"},
				},
			},
		}
		output := FormatProbeResults(results)
		if !strings.Contains(output, "Security headers [!!]") {
			t.Error("expected security header warning")
		}
		if !strings.Contains(output, "HSTS:") {
			t.Error("expected HSTS value in output")
		}
	})

	t.Run("ssl warning", func(t *testing.T) {
		days := 5
		expiry := time.Now().Add(5 * 24 * time.Hour)
		results := []ProbeResult{
			{URL: "https://expiring.com", Status: 200, OK: true, LatencyMs: 50, SSLDays: &days, SSLExpiry: &expiry},
		}
		output := FormatProbeResults(results)
		if !strings.Contains(output, "CRITICAL") {
			t.Error("expected CRITICAL for SSL < 7 days")
		}
	})
}

func TestFormatDNSResults(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		results := []DNSResult{
			{
				Hostname: "example.com",
				A:        []string{"93.184.216.34"},
				AAAA:     []string{"2606:2800:220:1:248:1893:25c8:1946"},
				CNAME:    "cdn.example.com",
				MX:       []string{"mail.example.com (priority 10)"},
			},
		}
		output := FormatDNSResults(results)
		if !strings.Contains(output, "[OK]") {
			t.Error("expected [OK]")
		}
		if !strings.Contains(output, "93.184.216.34") {
			t.Error("expected A record")
		}
		if !strings.Contains(output, "AAAA:") {
			t.Error("expected AAAA section")
		}
		if !strings.Contains(output, "CNAME:") {
			t.Error("expected CNAME section")
		}
		if !strings.Contains(output, "MX:") {
			t.Error("expected MX section")
		}
	})

	t.Run("error", func(t *testing.T) {
		results := []DNSResult{
			{Hostname: "nonexistent.invalid", Error: "no such host"},
		}
		output := FormatDNSResults(results)
		if !strings.Contains(output, "[!!]") {
			t.Error("expected [!!] for error")
		}
	})

	t.Run("no optional fields", func(t *testing.T) {
		results := []DNSResult{
			{Hostname: "simple.com", A: []string{"1.2.3.4"}},
		}
		output := FormatDNSResults(results)
		if strings.Contains(output, "CNAME:") {
			t.Error("should not show CNAME if empty")
		}
		if strings.Contains(output, "MX:") {
			t.Error("should not show MX if empty")
		}
	})
}

func TestProbeDNSReal(t *testing.T) {
	// Integration test — actually resolves DNS
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := ProbeDNS(ctx, []string{"google.com"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Error != "" {
		t.Skipf("DNS resolution failed (expected in sandboxed environments): %s", r.Error)
	}
	if len(r.A) == 0 && len(r.AAAA) == 0 {
		t.Error("expected at least one A or AAAA record for google.com")
	}
}
