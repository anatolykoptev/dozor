package httputil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrSSRFBlocked wraps every error CheckURL, GuardedDialContext, or a
// NewSSRFGuardedClient-wrapped transport returns when a target address is
// loopback, private (RFC1918 / RFC4193 ULA), link-local (including the cloud
// metadata address 169.254.169.254), unspecified, multicast, carrier-grade
// NAT (RFC 6598), or one of the IPv6 transition ranges that embed or route
// to an IPv4 address (NAT64, 6to4, the deprecated IPv4-compatible form) —
// the address classes an SSRF payload targets to reach internal
// infrastructure that must never be dialed from a caller-supplied target
// (e.g. an advertiser-provided website URL, a redirect Location header, an
// out-of-process render delegate's fetch target).
var ErrSSRFBlocked = errors.New("httputil: SSRF-blocked address")

const (
	guardedDialTimeout   = 10 * time.Second
	guardedDialKeepAlive = 30 * time.Second
)

// extraBlockedCIDRs are the ranges neither net.IP's built-in predicates
// (IsLoopback / IsPrivate / IsLinkLocalUnicast / IsLinkLocalMulticast /
// IsUnspecified / IsMulticast) cover:
//
//   - 100.64.0.0/10  CGNAT (RFC 6598) — shared address space carriers use
//     for NAT; not globally routable, frequently fronts internal services.
//   - 64:ff9b::/96   NAT64 well-known prefix (RFC 6052) — embeds an IPv4
//     address in the low 32 bits; blocking the whole prefix is simpler and
//     safer than unpacking and re-checking the embedded address.
//   - 2002::/16      6to4 (RFC 3056) — encodes a full IPv4 address in bits
//     16-47; deprecated and rare in legitimate traffic, so blocking the
//     entire range outright costs nothing.
//   - ::/96          IPv4-compatible IPv6 (deprecated, RFC 4291 §2.5.5.1,
//     distinct from the IPv4-MAPPED ::ffff:a.b.c.d form net.IP.To4()
//     already unwraps) — embeds an IPv4 address in the low 32 bits with an
//     all-zero high 96 bits.
var extraBlockedCIDRs = mustParseCIDRs(
	"100.64.0.0/10",
	"64:ff9b::/96",
	"2002::/16",
	"::/96",
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(fmt.Sprintf("httputil: invalid CIDR %q: %v", c, err))
		}
		nets = append(nets, n)
	}
	return nets
}

// allowedSchemes is the scheme allowlist CheckURL enforces. A caller that
// hands a raw URL to an out-of-process render delegate (a headless browser,
// a fetch microservice) must reject file:// and gopher:// etc. before
// dispatch — those schemes can read local files or speak arbitrary
// protocols to internal services, and no dial-time guard in THIS package
// can intervene once the delegate's own client executes them.
var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
}

// IsBlockedIP reports whether ip must never be dialed as a fetch/render
// target. This is the single, framework-owned SSRF block-list — every other
// primitive in this file (GuardedDialContext, NewSSRFGuardedClient,
// CheckURL) is built on top of this one predicate. A nil IP is treated as
// blocked (fail closed).
//
// Go's net.IP predicates already unwrap IPv4-mapped-IPv6 addresses (e.g.
// ::ffff:10.0.0.1 or ::ffff:127.0.0.1) to their IPv4 form before matching —
// including against extraBlockedCIDRs, since net.IPNet.Contains performs
// the same To4() unwrap internally — so no separate normalization step is
// needed here.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// Cloud-metadata address 169.254.169.254 is already covered by
	// IsLinkLocalUnicast (169.254.0.0/16); the explicit check below exists
	// purely to make the intent unmissable in review or a future refactor
	// of the link-local branch above.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) { //nolint:mnd // the well-known cloud-metadata address, not a tunable
		return true
	}
	for _, n := range extraBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// GuardedDialContext wraps base with a Control hook that refuses to connect
// to a blocked address (see IsBlockedIP). The check runs on the
// ALREADY-RESOLVED address at connect time — after DNS lookup, immediately
// before the connect(2) syscall — which is what defeats DNS-rebinding: a
// hostname that resolves to a public IP when net/http first looks it up but
// resolves to a private IP by the time this fires is still caught, because
// the check inspects the literal address about to be dialed, never the
// hostname string. Any pre-existing Control hook on base still runs first.
//
// base == nil uses a Dialer with sane guarded-fetch defaults (10s connect
// timeout, 30s keepalive).
func GuardedDialContext(base *net.Dialer) func(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	switch {
	case base != nil:
		d = *base // shallow copy: never mutate the caller's *net.Dialer
	default:
		d = net.Dialer{Timeout: guardedDialTimeout, KeepAlive: guardedDialKeepAlive}
	}
	prevControl := d.Control
	d.Control = func(network, address string, c syscall.RawConn) error {
		if prevControl != nil {
			if err := prevControl(network, address, c); err != nil {
				return err
			}
		}
		return DenyBlockedAddress(network, address)
	}
	return d.DialContext
}

// DenyBlockedAddress is the Control-hook body: it inspects the
// ALREADY-RESOLVED network/address pair — exactly what net.Dialer.Control
// receives, and what GuardedDialContext wires this into — and refuses
// anything IsBlockedIP flags. Exported (split out from GuardedDialContext
// originally so tests could drive it directly with a hardcoded
// post-resolution address, simulating exactly what net/http passes after DNS
// lookup, without needing a real DNS rebind) so a caller wiring a bespoke
// Control-hook-shaped seam on an opaque transport this package does not
// itself dial (e.g. a stealth/fingerprint-evasion client's own dialer hook)
// can reuse the identical policy GuardedDialContext uses internally, rather
// than duplicating it. See SSRFGuards for the paired redirect-guard closure.
func DenyBlockedAddress(network, address string) error {
	switch network {
	case "unix", "unixgram", "unixpacket":
		// A Unix domain socket dials a local filesystem path, not a network
		// address (address is e.g. "/var/run/sidecar.sock", not host:port) —
		// it is not an SSRF-to-internal-IP vector, since the caller already
		// chose a specific local socket path rather than a resolvable
		// hostname. Blocking it here would make this guard unusable for a
		// future framework consumer whose base Transport dials a UDS
		// sidecar (a legitimate, increasingly common pattern).
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address // no port present — shouldn't happen for a tcp/udp dial target
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A resolved dial target that isn't a literal IP is unexpected; fail
		// closed rather than let an unparseable address through.
		return fmt.Errorf("%w: cannot parse dial address %q (%s)", ErrSSRFBlocked, address, network)
	}
	if IsBlockedIP(ip) {
		return fmt.Errorf("%w: %s (%s)", ErrSSRFBlocked, ip, network)
	}
	return nil
}

// guardedTransport returns an *http.Transport cloned from
// http.DefaultTransport (preserving its proxy / idle-conn / HTTP2 defaults —
// NOTE this means Proxy: http.ProxyFromEnvironment is preserved too, which
// is exactly the case wrapTransportTier's outer CheckURL layer exists to
// cover) with DialContext replaced by GuardedDialContext, so every DIRECT
// (non-proxied) connection made through it is SSRF-safe at connect time.
func guardedTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // http.DefaultTransport is always *http.Transport per stdlib contract
	t.DialContext = GuardedDialContext(&net.Dialer{Timeout: guardedDialTimeout, KeepAlive: guardedDialKeepAlive})
	return t
}

// wrapTransportTier composes the pre-request CheckURL guard (guardedRoundTripper)
// on top of an already dial-guarded *http.Transport t.
//
// Why both layers are needed: when t.Proxy is non-nil (either explicitly set
// by a caller, or inherited from http.DefaultTransport's
// Proxy: http.ProxyFromEnvironment via guardedTransport()/Clone()), net/http
// calls DialContext with the PROXY's host:port, not the real destination —
// the real target lives in the request line (plain HTTP) or the CONNECT
// tunnel target (HTTPS), and t.DialContext (GuardedDialContext) never sees
// it. A proxied request to an internal target would sail straight through
// the connect-time guard, which only ever inspects the (public, allowed)
// proxy address. guardedRoundTripper's CheckURL(req.URL) runs BEFORE t is
// even reached and always evaluates the real destination URL regardless of
// whether the request ends up proxied, closing that hole.
//
// Net effect per request:
//   - direct (no proxy in play): BOTH tiers fire — the pre-request CheckURL
//     here, plus GuardedDialContext's connect-time, DNS-rebind-proof check
//     already wired into t.
//   - proxied: only the pre-request CheckURL fires (the same pre-resolve
//     tier CheckURL's own GoDoc describes — weaker than connect-time against
//     DNS-rebind, but real: it refuses the request outright for a target
//     that resolves blocked at check time). http.Client re-invokes
//     RoundTrip on every redirect hop, so each hop's real destination is
//     re-checked too.
func wrapTransportTier(t *http.Transport) http.RoundTripper {
	return &guardedRoundTripper{next: t}
}

// NewSSRFGuardedClient returns an SSRF-guarded *http.Client built on top of
// base. base == nil returns a fresh client with a guardedTransport().
//
// Two composition tiers, chosen by what base.Transport actually is:
//
//   - nil or *http.Transport: cloned (Clone() preserves TLSClientConfig,
//     proxy, HTTP2 settings, MaxIdleConns, etc. — every field a caller may
//     have set) with DialContext replaced by GuardedDialContext, THEN
//     wrapped with the pre-request CheckURL layer (wrapTransportTier) — see
//     wrapTransportTier's doc for why a proxy-configured Transport needs
//     both layers, not just the dial-time one.
//   - any other http.RoundTripper (e.g. a stealth/fingerprint-evasion
//     client whose Transport performs its own dial via a bespoke backend,
//     with no DialContext/net.Dialer hook exposed at all): wrapped with
//     ONLY the pre-request CheckURL layer — the necessarily WEAKER
//     pre-resolve tier (a DNS-rebind can still occur between this check and
//     the delegate's own, separate resolution), but the best guarantee
//     available without reaching into a dial mechanism this package does
//     not own.
//
// The returned *http.Client is a shallow copy of base; base itself (and its
// Transport) is never mutated.
func NewSSRFGuardedClient(base *http.Client) *http.Client {
	if base == nil {
		return &http.Client{Transport: wrapTransportTier(guardedTransport())}
	}
	cc := *base
	switch t := base.Transport.(type) {
	case nil:
		cc.Transport = wrapTransportTier(guardedTransport())
	case *http.Transport:
		tc := t.Clone()
		tc.DialContext = GuardedDialContext(&net.Dialer{Timeout: guardedDialTimeout, KeepAlive: guardedDialKeepAlive})
		cc.Transport = wrapTransportTier(tc)
	default:
		cc.Transport = &guardedRoundTripper{next: t}
	}
	return &cc
}

// guardedRoundTripper wraps an arbitrary http.RoundTripper with a
// pre-request SSRF check (see CheckURL) on the outbound request's URL. This
// composes with ANY RoundTripper implementation — including a dial-guarded
// *http.Transport (see wrapTransportTier, which layers this on top of the
// *http.Transport tier too, to cover the proxy case) — it never touches the
// wrapped one's internal dial mechanics, so a stealth/fingerprint-evasion
// implementation is untouched on the allow path.
type guardedRoundTripper struct {
	next http.RoundTripper
}

func (g *guardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckURL(req.Context(), req.URL); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(req)
}

// CheckURL is a pre-handoff safety check for a URL this package does not
// itself dial — e.g. before handing an "any place" URL to an out-of-process
// render delegate (a headless browser, a fetch microservice) whose own
// outbound dial GuardedDialContext / NewSSRFGuardedClient cannot reach.
// Call it as close as possible to the point of dispatch, and again after
// each redirect hop the delegate reports, to minimize the DNS-rebind
// window — CheckURL is necessarily weaker against rebinding than
// GuardedDialContext, since DNS can change between this resolution and the
// delegate's own, separate one.
//
// Enforces two things:
//  1. Scheme allowlist — only "http" and "https" are permitted; a headless
//     browser coerced into "file://" or "gopher://" bypasses every IP-based
//     check below, since those schemes never dial the checked host at all.
//  2. Every resolved address for the URL's host passes IsBlockedIP.
//
// A host that is a literal IP is checked directly. A host that LOOKS like a
// non-standard numeral encoding of an IP (decimal, octal, or hex — e.g.
// "2130706433", "0x7f000001", or "012.0.0.1") but fails net.ParseIP is
// refused outright rather than handed to DNS resolution: some resolvers
// (notably glibc's getaddrinfo via cgo) still parse these forms as literal
// IPs, which would silently defeat this check if it fell through to a
// same-string-but-different DNS lookup.
func CheckURL(ctx context.Context, u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: nil URL", ErrSSRFBlocked)
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("%w: scheme %q not allowed (http/https only)", ErrSSRFBlocked, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrSSRFBlocked)
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, ip)
		}
		return nil
	}
	if looksLikeAltEncodedIP(host) {
		return fmt.Errorf("%w: host %q looks like a non-standard IP encoding", ErrSSRFBlocked, host)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("httputil: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %q resolved to no addresses", ErrSSRFBlocked, host)
	}
	for _, a := range addrs {
		if IsBlockedIP(a.IP) {
			return fmt.Errorf("%w: %s resolves to %s", ErrSSRFBlocked, host, a.IP)
		}
	}
	return nil
}

// CheckRawURL parses rawURL and delegates to CheckURL. Convenience for
// callers holding a string (a redirect Location header, an MCP tool
// argument) rather than an already-parsed *url.URL.
func CheckRawURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: parse %q: %w", ErrSSRFBlocked, rawURL, err)
	}
	return CheckURL(ctx, u)
}

// maxSSRFRedirectHops caps how many redirect hops the redirect closure
// SSRFGuards returns will follow before refusing. Owned ONCE here, not
// duplicated per consumer: installing a custom redirect-decision hook (a
// stdlib http.Client.CheckRedirect, or an adapter around a non-stdlib
// equivalent) REPLACES net/http's own built-in 10-hop cap, not just its SSRF
// behavior. A consumer that re-implements the SSRF check but forgets this
// trades a bounded redirect chain for an unbounded one — a self-redirecting
// or looping target then hangs the caller until its own request timeout
// instead of failing fast. Keeping the cap here, next to the check it
// replaces, means every consumer gets it for free and identically.
const maxSSRFRedirectHops = 10

// SSRFGuards returns the pair of stdlib-typed closures an opaque-transport
// HTTP client (one whose Transport performs its own dial/redirect via a
// bespoke backend this package cannot reach — e.g. a stealth/
// fingerprint-evasion client) installs to close the SAME two gaps
// NewSSRFGuardedClient already closes for a plain *http.Transport-backed
// client:
//
//   - dial is DenyBlockedAddress itself — wire it into whatever
//     Control-hook-shaped seam the opaque transport exposes (directly, if it
//     accepts a net.Dialer.Control-shaped func, or behind a thin adapter
//     otherwise) for the rebind-proof, connect-time check.
//   - redirect enforces the ≤maxSSRFRedirectHops cap (see that const's doc
//     for why re-owning it is required, not optional) and THEN
//     CheckURL(req.Context(), req.URL) on every hop — wire it into whatever
//     per-hop redirect-decision seam the opaque transport exposes. Its
//     signature matches http.Client.CheckRedirect exactly, so a
//     stdlib-backed caller can assign it directly with no adapter.
//
// Named for the CAPABILITY (this package's framework-owned SSRF policy), not
// any one consumer — this package takes no dependency on whatever
// opaque-transport client ends up wiring these in, and the identical pair of
// closures can be handed to more than one backend so their behavior cannot
// drift apart.
func SSRFGuards() (
	redirect func(req *http.Request, via []*http.Request) error,
	dial func(network, address string) error,
) {
	redirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxSSRFRedirectHops {
			return fmt.Errorf("httputil: stopped after %d redirects", maxSSRFRedirectHops)
		}
		return CheckURL(req.Context(), req.URL)
	}
	dial = DenyBlockedAddress
	return redirect, dial
}

// looksLikeAltEncodedIP reports whether host resembles an alternate-encoding
// numeric IP literal (hex, pure-decimal, or octal-per-component) that
// net.ParseIP rejects but a permissive resolver may still interpret as an IP
// address — a classic SSRF filter bypass technique.
func looksLikeAltEncodedIP(host string) bool {
	if host == "" {
		return false
	}
	if strings.Contains(strings.ToLower(host), "0x") {
		return true
	}
	allDigits := true
	for _, r := range host {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		// Pure-decimal integer form, e.g. "2130706433" == 127.0.0.1.
		return true
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) < 2 || part[0] != '0' {
			continue
		}
		numeric := true
		for _, r := range part {
			if r < '0' || r > '9' {
				numeric = false
				break
			}
		}
		if numeric {
			// Octal-looking dotted component, e.g. "012.0.0.1".
			return true
		}
	}
	return false
}
