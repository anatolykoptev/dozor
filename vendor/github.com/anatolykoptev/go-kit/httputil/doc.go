// Package httputil provides small HTTP helpers shared across go-kit services.
//
// # Security Headers
//
// SecurityHeaders sets a conservative set of HTTP security headers on a
// ResponseWriter. Defaults follow go-nerv stricter policy:
//
//   - Content-Security-Policy: default-src self; script-src self; style-src self unsafe-inline
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Permissions-Policy: camera=(), microphone=(), geolocation=()
//   - X-XSS-Protection: 0
//
// Cache-Control is NOT set by default. Cache policy is orthogonal to security
// headers — marketing pages, API endpoints, and authed admin pages have
// fundamentally different caching requirements. Use WithCacheControl to set it:
//
//	SecurityHeaders(w, WithCacheControl("no-store"))           // authed admin
//	SecurityHeaders(w, WithCacheControl("public, max-age=3600")) // public assets
//
// Services that require a less restrictive CSP (e.g. oxpulse-admin, which
// needs unsafe-inline for its embedded scripts) should call
// SecurityHeaders(w, WithCSP("...")) to override only that header while
// keeping all other defaults.
//
// # Client IP
//
// ClientIP extracts the real client address from an HTTP request. Consults
// X-Real-IP, then X-Forwarded-For (first hop), then r.RemoteAddr. Each
// candidate is validated via net.ParseIP -- invalid or spoofed header values
// are silently skipped rather than returned. See ClientIP for the full trust
// model and deployment requirements.
//
// # SSRF Guard
//
// This is the single, framework-owned SSRF block-list for any go-kit
// service that fetches a caller-supplied URL (an image src, an advertiser
// website, a redirect Location header, an out-of-process render delegate's
// target). It supersedes the two guards it consolidates -- render/html's
// former unexported isPrivateIP/safeDial and go-enriche's fetch/ssrf.go --
// and adds the ranges neither one covered: CGNAT (100.64.0.0/10), the NAT64
// well-known prefix (64:ff9b::/96), 6to4 (2002::/16), and the deprecated
// IPv4-compatible IPv6 form (::/96).
//
// IsBlockedIP is the low-level predicate: loopback, RFC1918/RFC4193
// private, link-local (incl. the 169.254.169.254 cloud-metadata address),
// unspecified, multicast, plus the four ranges above. Every other function
// here is built on top of it.
//
// GuardedDialContext wraps a *net.Dialer with a Control hook that checks
// the ALREADY-RESOLVED address at connect time, immediately before the
// connect(2) syscall -- this defeats DNS-rebinding, since the check
// inspects the literal address about to be dialed, never a hostname string
// that could resolve differently between lookup and connect. Non-TCP/UDP
// dials -- a Unix domain socket ("unix"/"unixgram"/"unixpacket") -- pass
// through unchecked: a UDS dials a local filesystem path, not a network
// address, so it is not an SSRF-to-internal-IP vector.
//
// NewSSRFGuardedClient wraps an *http.Client in two tiers depending on its
// Transport: a *http.Transport (or nil) gets DialContext replaced with
// GuardedDialContext AND is wrapped in a pre-request CheckURL check; any
// other http.RoundTripper (e.g. a stealth/fingerprint-evasion client with no
// exposed dial hook) gets ONLY the pre-request CheckURL check. Both layers
// are needed on the *http.Transport tier because a proxy-configured
// Transport (explicitly, or inherited via Proxy: http.ProxyFromEnvironment)
// causes net/http to call DialContext with the PROXY's address, never the
// real target -- GuardedDialContext alone would silently pass a proxied
// request to an internal host straight through. So: a direct (non-proxied)
// request gets BOTH the connect-time, DNS-rebind-proof dial guard AND the
// pre-request check; a proxied request gets only the pre-request check
// (still real protection -- it evaluates the actual destination URL, not
// the proxy's -- but a necessarily weaker, pre-resolve tier, the same one
// CheckURL itself provides for a delegate this package cannot dial-guard).
//
// CheckURL is the pre-handoff check for a URL this package never dials
// itself -- e.g. before handing an "any place" URL to an out-of-process
// render delegate. It enforces an http/https scheme allowlist (a headless
// browser coerced into file:// or gopher:// bypasses every IP-based check,
// since those schemes never dial the checked host at all) plus IsBlockedIP
// on every resolved address. Call it again after each redirect hop a
// delegate reports, to minimize the DNS-rebind window. A host that looks
// like a non-standard IP encoding (decimal, octal, or hex -- e.g.
// "2130706433" or "0x7f000001") but fails net.ParseIP is refused outright
// rather than handed to DNS resolution, since some resolvers still parse
// those forms as literal IPs.
package httputil
