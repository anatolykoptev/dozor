package httputil

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP extracts the real client address. Services behind a reverse proxy
// (e.g. Caddy) typically see only the loopback address in r.RemoteAddr.
//
// Priority:
//  1. X-Real-IP — set by the proxy to the single trusted client IP; most authoritative.
//  2. X-Forwarded-For first element — fallback for proxies that don't set X-Real-IP.
//  3. r.RemoteAddr — last resort (always loopback behind a proxy, but correct in tests).
//
// Each candidate is validated via net.ParseIP before use; an invalid value
// silently falls through to the next candidate.
//
// # Security / trust model
//
// ClientIP trusts the X-Real-IP and X-Forwarded-For headers when their values
// parse as valid IPs. These headers are client-controlled and can be spoofed
// by any caller that can reach the service directly. Callers MUST ensure the
// service is fronted by a reverse proxy that strips or overrides these headers
// from untrusted sources before calling ClientIP. Behind a misconfigured
// deployment the returned IP is attacker-controlled.
func ClientIP(r *http.Request) string {
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		candidate := strings.TrimSpace(xri)
		if ip := net.ParseIP(candidate); ip != nil {
			return ip.String()
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		candidate := strings.TrimSpace(parts[0])
		if ip := net.ParseIP(candidate); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
