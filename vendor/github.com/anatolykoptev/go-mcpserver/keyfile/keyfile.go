package keyfile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
)

// extraToolsKey is the TokenInfo.Extra key carrying the verified key's tool
// list. ToolFilter reads nothing else.
const extraToolsKey = "keyfile.tools"

// extraAttrsKey carries the verified key's consumer-defined attrs.
const extraAttrsKey = "keyfile.attrs"

const (
	defaultStatTTL = time.Second
	retryDelay     = 50 * time.Millisecond
	maxTokenTTL    = time.Hour
	rejectLogEvery = time.Minute
)

// reservedExact are the metric-label sentinels, never valid key names.
var reservedExact = []string{rejectLabelUnknown, "anonymous"}

var reservedPrefixRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// Options configures a [Store].
type Options struct {
	// Path is the key file. A missing file means no keys (deny all).
	Path string
	// Prefix is the token prefix, [a-z]{2,8}; "dzk" yields tokens "dzk1_...".
	Prefix string
	// Tools are the tool names registered on the server (required, non-empty).
	// A key-file entry that names any other tool is invalid and dropped.
	Tools []string
	// ReservedPrefixes are consumer-reserved key-name prefixes (e.g. "env-").
	// "unknown" and "anonymous" are always reserved.
	ReservedPrefixes []string
	// ValidateAttrs, if set, is called for every otherwise-valid entry on every
	// (re)load with the entry name and a copy of its attrs. A non-nil error (or
	// a panic) drops only that entry; the error text is logged next to the entry
	// name, so it must not contain secrets. The library never interprets attrs
	// itself.
	//
	// An absent attrs map is an empty map, not an error: a consumer that relies
	// on restrictive attrs (a role, a run-as identity) should REQUIRE them here
	// and reject entries that omit them, so a forgotten field cannot grant
	// default privileges.
	ValidateAttrs func(name string, attrs map[string]string) error
	// AllowNonToolMethods disables the default-deny of MCP methods other than
	// initialize, ping, server/discover, notifications/*, tools/list and
	// tools/call (resources/*, prompts/*,
	// completion/complete, logging/setLevel, ...) in [Store.Middleware]. The
	// key file scopes tools only, so leave this off unless every resource and
	// prompt on the server is safe for every key.
	AllowNonToolMethods bool
	// Logger receives reload and reject diagnostics. Nil = slog.Default().
	Logger *slog.Logger
}

// Status is a point-in-time view of the store.
type Status struct {
	// LastReloadOK is false while the store is in deny-all after a failed read.
	LastReloadOK bool
	// FileMissing is true when the key file does not exist (an empty, healthy key set).
	FileMissing bool
	// Keys is the number of valid entries currently loaded.
	Keys int
	// FileSHA256 is the hex SHA-256 of the loaded file content ("" if missing).
	FileSHA256 string
	// Rejected counts rejected tokens by reason since Open.
	Rejected Rejected
}

// Rejected counts rejected bearer tokens by reason.
type Rejected struct {
	Malformed, Unknown, Expired uint64
}

// Store verifies tokens against a key file. Create it with [Open].
type Store struct {
	path         string
	prefix       string
	tools        map[string]struct{}
	reserved     []string
	logger       *slog.Logger
	validator    func(string, map[string]string) error
	allowNonTool bool

	snap       atomic.Pointer[snapshot]
	reloading  atomic.Bool
	lastStatNS atomic.Int64

	rejects       [reasonCount]atomic.Uint64
	lastWarn      [reasonCount]atomic.Int64
	lastIOWarn    atomic.Int64
	lastLoadWarn  atomic.Int64
	lastRetryWarn atomic.Int64

	// test seams
	statTTL  time.Duration
	now      func() time.Time
	readFile func(string) ([]byte, error)
	sleep    func(time.Duration)
	euid     func() int
	lookups  atomic.Uint64 // hash lookups performed
	reloads  atomic.Uint64 // reload cycles started
}

type rejectReason int

const (
	reasonMalformed rejectReason = iota
	reasonUnknown
	reasonExpired
	reasonCount
)

const rejectLabelUnknown = "unknown"

func (r rejectReason) String() string {
	return [...]string{"malformed", rejectLabelUnknown, "expired"}[r]
}

// Open validates o and loads the key file. It returns an error only for invalid
// Options; a missing or invalid file yields a store that denies everything.
func Open(o Options) (*Store, error) {
	if o.Path == "" {
		return nil, errors.New("keyfile: Path is required")
	}
	if !prefixRE.MatchString(o.Prefix) {
		return nil, fmt.Errorf("keyfile: Prefix must match %s", prefixRE)
	}
	if len(o.Tools) == 0 {
		return nil, errors.New("keyfile: Tools is required (the registered tool names)")
	}
	tools := make(map[string]struct{}, len(o.Tools))
	for _, t := range o.Tools {
		if t == "" || t == "*" {
			return nil, fmt.Errorf("keyfile: invalid tool name %q in Options.Tools", t)
		}
		if _, dup := tools[t]; dup {
			return nil, fmt.Errorf("keyfile: duplicate tool %q in Options.Tools", t)
		}
		tools[t] = struct{}{}
	}
	for _, p := range o.ReservedPrefixes {
		if !reservedPrefixRE.MatchString(p) {
			return nil, fmt.Errorf("keyfile: invalid reserved prefix %q", p)
		}
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{
		path:         o.Path,
		prefix:       o.Prefix,
		tools:        tools,
		reserved:     slices.Clone(o.ReservedPrefixes),
		logger:       logger,
		validator:    o.ValidateAttrs,
		allowNonTool: o.AllowNonToolMethods,
		statTTL:      defaultStatTTL,
		now:          time.Now,
		sleep:        time.Sleep,
		euid:         os.Geteuid,
	}
	s.readFile = s.readChecked
	s.snap.Store(denyAll(fileSig{}, "", false))
	s.reloading.Store(true)
	s.reloads.Add(1)
	s.reloadBlocking()
	return s, nil
}

// Status reports the current key set and reject counters.
func (s *Store) Status() Status {
	cur := s.snap.Load()
	return Status{
		LastReloadOK: cur.ok,
		FileMissing:  cur.sig.missing,
		Keys:         len(cur.keys),
		FileSHA256:   cur.fileSHA,
		Rejected: Rejected{
			Malformed: s.rejects[reasonMalformed].Load(),
			Unknown:   s.rejects[reasonUnknown].Load(),
			Expired:   s.rejects[reasonExpired].Load(),
		},
	}
}

// BearerAuth returns the verifier and the tool filter as one pair. Prefer it
// (or [Store.Install]) over wiring [Store.Verifier] alone: a verifier without
// the filter authenticates a key but scopes nothing.
//
// BearerAuth does not include [Store.Middleware]; add that to
// Config.MCPReceivingMiddleware yourself, or use Install which does both.
func (s *Store) BearerAuth() *mcpserver.BearerAuth {
	return &mcpserver.BearerAuth{Verifier: s.Verifier(), ToolFilter: s.ToolFilter()}
}

// Install wires this store into cfg: BearerAuth (verifier plus tool filter) and
// the [Store.Middleware] receiving middleware. It refuses to overwrite an
// existing BearerAuth and refuses a Config with RESTBridge set, which this
// package does not support. (Setting RESTBridge after Install still fails
// closed: bridged calls carry no identity and are denied.)
func (s *Store) Install(cfg *mcpserver.Config) error {
	if cfg == nil {
		return errors.New("keyfile: Install: nil Config")
	}
	if cfg.BearerAuth != nil {
		return errors.New("keyfile: Install: Config.BearerAuth is already set")
	}
	if cfg.RESTBridge {
		return errors.New("keyfile: Install: RESTBridge is not supported with keyfile")
	}
	cfg.BearerAuth = s.BearerAuth()
	cfg.MCPReceivingMiddleware = append(cfg.MCPReceivingMiddleware, s.Middleware())
	return nil
}

// Verifier returns the token verifier. Every rejection wraps
// [auth.ErrInvalidToken] (HTTP 401) and carries no detail about why. The
// returned TokenInfo has UserID = key name and private copies of the key's tool
// list and attrs.
//
// WARNING: a Verifier on its own only authenticates. It must be paired with
// [Store.ToolFilter] in the same BearerAuth, or every valid key can call every
// tool. Use [Store.BearerAuth] or [Store.Install] to get the pair.
func (s *Store) Verifier() auth.TokenVerifier {
	return s.verify
}

func (s *Store) verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	s.maybeReload()
	if !wellFormed(s.prefix, token) {
		s.reject(reasonMalformed, "")
		return nil, auth.ErrInvalidToken
	}
	s.lookups.Add(1)
	// The map is keyed by the SHA-256 of the token. An attacker cannot steer
	// the digest, so lookup timing reveals nothing about stored digests.
	digest := sha256.Sum256([]byte(token))
	e, found := s.snap.Load().keys[digest]
	if !found {
		s.reject(reasonUnknown, "")
		return nil, auth.ErrInvalidToken
	}
	now := s.now()
	if !now.Before(e.expires) {
		s.reject(reasonExpired, e.name)
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		UserID:     e.name,
		Expiration: minTime(e.expires, now.Add(maxTokenTTL)),
		Extra: map[string]any{
			extraToolsKey: slices.Clone(e.tools),
			extraAttrsKey: maps.Clone(e.attrs),
		},
	}, nil
}

// ToolFilter returns a BearerAuth.ToolFilter that allows a tool only when it is
// in the tool snapshot carried by the verified TokenInfo. A nil TokenInfo, or
// one without the snapshot, is denied; the store itself is never consulted.
//
// WARNING: it must be paired with [Store.Verifier]; see the warning there. Use
// [Store.BearerAuth] or [Store.Install].
func (s *Store) ToolFilter() func(context.Context, string, *mcpserver.TokenInfo) bool {
	return func(_ context.Context, tool string, info *mcpserver.TokenInfo) bool {
		if info == nil {
			return false
		}
		granted, ok := info.Extra[extraToolsKey].([]string)
		if !ok {
			return false
		}
		return allow(granted, tool)
	}
}

// ctxKey is keyfile's own context key for the per-request identity.
type ctxKey struct{}

// Middleware returns MCP receiving middleware that (1) denies MCP methods other
// than initialize, ping, server/discover, notifications/*, tools/list and
// tools/call unless Options.AllowNonToolMethods is set, and (2) stores each request's own
// verified TokenInfo in the context for [Attrs]. [Store.Install] adds it.
//
// The key file scopes tools only, and the tool filter never sees resources or
// prompts, so without this guard any valid key could read every registered
// resource and prompt.
func (s *Store) Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if !s.allowNonTool && !scopedMethod(method) {
				return nil, fmt.Errorf("keyfile: method not permitted: %s", method)
			}
			if extra := req.GetExtra(); extra != nil && extra.TokenInfo != nil {
				ctx = context.WithValue(ctx, ctxKey{}, extra.TokenInfo)
			}
			return next(ctx, method, req)
		}
	}
}

// scopedMethod reports whether method is handled by the tool filter or carries
// no key-scoped data. tools/* is matched exactly (list and call): a future
// tools/* method is default-denied until it is reviewed. server/discover is
// sent by clients before initialize and returns server info only.
func scopedMethod(method string) bool {
	switch method {
	case "initialize", "ping", "server/discover", "tools/list", "tools/call":
		return true
	}
	return strings.HasPrefix(method, "notifications/")
}

// AttrsOf returns a copy of the attrs snapshotted when info was verified, or nil
// if info is nil or was not produced by a keyfile Verifier. In a tool handler
// prefer AttrsOf(req.Extra.TokenInfo), which is always the current request's
// identity.
func AttrsOf(info *mcpserver.TokenInfo) map[string]string {
	if info == nil {
		return nil
	}
	attrs, ok := info.Extra[extraAttrsKey].(map[string]string)
	if !ok {
		return nil
	}
	return maps.Clone(attrs)
}

// Attrs returns a copy of the verified key's attrs for the request in ctx, or
// nil when ctx carries no keyfile identity (for example when [Store.Middleware]
// is not installed). It reads a context value set per request by the
// middleware, not the SDK's connection context, which in stateful mode would
// still hold the identity of the request that opened the session.
func Attrs(ctx context.Context) map[string]string {
	info, _ := ctx.Value(ctxKey{}).(*mcpserver.TokenInfo)
	return AttrsOf(info)
}

// allow reports whether tool is explicitly granted. An empty list grants
// nothing and "*" is never a wildcard.
func allow(granted []string, tool string) bool {
	if tool == "" || tool == "*" {
		return false
	}
	return slices.Contains(granted, tool)
}

// reject counts a rejected token and emits at most one log line per reason per
// minute. The line carries the reason and, when known, the key name; never any
// token material.
func (s *Store) reject(r rejectReason, keyName string) {
	s.rejects[r].Add(1)
	if !s.warnDue(&s.lastWarn[r]) {
		return
	}
	attrs := []any{slog.String("reason", r.String())}
	if keyName != "" {
		attrs = append(attrs, slog.String("key", keyName))
	}
	s.logger.Warn("keyfile: token rejected", attrs...)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
