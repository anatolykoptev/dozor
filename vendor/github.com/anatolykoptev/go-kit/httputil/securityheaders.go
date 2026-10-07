package httputil

import "net/http"

// Default security header values. The CSP default is the stricter go-nerv
// policy (script-src 'self', no 'unsafe-inline'). Callers that need a more
// permissive policy pass WithCSP explicitly.
//
// defaultPermissionsPolicy restricts camera, microphone, and geolocation —
// appropriate for admin and API UIs that don't need device access.
//
// defaultXSSProtection is "0" per OWASP and Mozilla Observatory: the legacy
// XSS auditor is disabled in modern browsers and is itself an XSS vector in
// older ones. Setting "0" explicitly ensures any residual browser behaviour
// is switched off.
//
// BREAKING CHANGE (v0.56.0): Cache-Control is no longer set by default.
// SecurityHeaders focuses on security headers; cache policy is orthogonal and
// must be declared explicitly by each handler. Callers that relied on the
// implicit "no-store" must either:
//   - pass WithCacheControl("no-store") to SecurityHeaders, or
//   - call w.Header().Set("Cache-Control", "no-store") directly.
const (
	// defaultCSP includes 'self' in style-src so that <link rel="stylesheet">
	// served from the same origin loads. The prior default
	// (`style-src 'unsafe-inline'` without 'self') permitted inline <style>
	// blocks but blocked every <link rel="stylesheet"> — including own assets.
	// Confirmed via go-nerv admin pm7.css browser block (2026-05-22).
	// Callers needing a stricter or different policy pass WithCSP explicitly.
	defaultCSP               = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'"
	defaultReferrerPolicy    = "strict-origin-when-cross-origin"
	defaultPermissionsPolicy = "camera=(), microphone=(), geolocation=()"
	defaultXSSProtection     = "0"
)

// config holds overridable header values.
type config struct {
	csp               string
	referrerPolicy    string
	permissionsPolicy string
	xssProtection     string
	cacheControl      string // empty = do not set Cache-Control
}

// Option modifies the header configuration applied by SecurityHeaders.
type Option func(*config)

// WithCSP overrides the Content-Security-Policy header value.
func WithCSP(policy string) Option {
	return func(c *config) { c.csp = policy }
}

// WithReferrerPolicy overrides the Referrer-Policy header value.
func WithReferrerPolicy(policy string) Option {
	return func(c *config) { c.referrerPolicy = policy }
}

// WithPermissionsPolicy overrides the Permissions-Policy header value.
// Default: "camera=(), microphone=(), geolocation=()" (no device access).
func WithPermissionsPolicy(policy string) Option {
	return func(c *config) { c.permissionsPolicy = policy }
}

// WithXSSProtection overrides the X-XSS-Protection header value.
// Default: "0" (disable legacy XSS auditor per OWASP/Mozilla Observatory).
func WithXSSProtection(value string) Option {
	return func(c *config) { c.xssProtection = value }
}

// WithCacheControl sets the Cache-Control header value.
// SecurityHeaders does not set Cache-Control by default — cache policy is
// orthogonal to security headers and must be declared per handler.
// Use this option when the caller wants a single call to cover both concerns:
//
//	SecurityHeaders(w, WithCacheControl("no-store"))          // authed admin pages
//	SecurityHeaders(w, WithCacheControl("public, max-age=3600")) // public assets
func WithCacheControl(value string) Option {
	return func(c *config) { c.cacheControl = value }
}

// SecurityHeaders writes a conservative set of HTTP security headers to w.
// Each header is set via Header().Set (replaces, never appends).
// Pass Option values to override specific headers from their defaults.
//
// Headers set by default:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Content-Security-Policy: default-src 'self'; script-src 'self'; ...
//   - Permissions-Policy: camera=(), microphone=(), geolocation=()
//   - X-XSS-Protection: 0
//
// Cache-Control is NOT set by default. Pass WithCacheControl to set it.
// Each handler must declare its own cache policy — marketing pages, API
// endpoints, and authed admin pages have fundamentally different requirements.
func SecurityHeaders(w http.ResponseWriter, opts ...Option) {
	cfg := config{
		csp:               defaultCSP,
		referrerPolicy:    defaultReferrerPolicy,
		permissionsPolicy: defaultPermissionsPolicy,
		xssProtection:     defaultXSSProtection,
	}
	for _, o := range opts {
		o(&cfg)
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", cfg.referrerPolicy)
	h.Set("Content-Security-Policy", cfg.csp)
	if cfg.cacheControl != "" {
		h.Set("Cache-Control", cfg.cacheControl)
	}
	h.Set("Permissions-Policy", cfg.permissionsPolicy)
	h.Set("X-XSS-Protection", cfg.xssProtection)
}
