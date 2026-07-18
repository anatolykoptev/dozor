package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const (
	requestIDHeader = "X-Request-ID"
	idBytes         = 16
)

// validRequestID accepts the same charset used by tracing systems
// (Datadog, OpenTelemetry, AWS X-Ray): URL-safe ASCII, no control chars.
// Anything else is treated as untrusted and replaced.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type requestIDContextKey struct{}

// Middleware is an HTTP middleware that wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware in order: the first argument becomes the outermost
// wrapper, so requests pass through mw[0] → mw[1] → … → handler.
func Chain(handler http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		handler = mw[i](handler)
	}
	return handler
}

// Recovery returns middleware that recovers from panics and returns 500.
func Recovery(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					logger.Error("panic recovered",
						slog.Any("panic", rv),
						slog.String("path", r.URL.Path),
						slog.String("stack", string(debug.Stack())),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID returns middleware that generates or propagates X-Request-ID.
//
// If the incoming request has the header AND the value matches
// [A-Za-z0-9_-]{1,64}, it is reused. Otherwise — empty, too long, or
// containing characters that could confuse log parsers (newlines, control
// chars, quotes) — a fresh random hex ID is generated. This prevents
// log-forging via attacker-supplied request IDs.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if !validRequestID.MatchString(id) {
				id = generateID()
			}
			w.Header().Set(requestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext retrieves the request ID stored by [RequestID] middleware.
// Returns an empty string if no ID is present.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestID returns a copy of ctx with the given request ID.
// Use in tests or when manually injecting a request ID without middleware.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestLog returns middleware that logs method, path, status, duration,
// and request_id for every request at Info level.
//
// Equivalent to RequestLogWithSkip(logger, nil) — no paths skipped.
func RequestLog(logger *slog.Logger) Middleware {
	return RequestLogWithSkip(logger, nil)
}

// RequestLogWithSkip returns middleware that logs method, path, status,
// duration, and request_id for every request. Paths in skipPaths are demoted
// to Debug level so liveness/metrics traffic does not flood Info-level
// access logs. nil or empty skipPaths means log everything at Info.
func RequestLogWithSkip(logger *slog.Logger, skipPaths []string) Middleware {
	skip := make(map[string]struct{}, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w}
			rw.status.Store(int32(http.StatusOK))
			next.ServeHTTP(rw, r)
			level := slog.LevelInfo
			if _, ok := skip[r.URL.Path]; ok {
				level = slog.LevelDebug
			}
			logger.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", int(rw.status.Load())),
				slog.Int64("bytes", rw.bytesWritten.Load()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFromContext(r.Context())),
			)
		})
	}
}

const defaultAllowHeaders = "Content-Type, Authorization, X-Request-ID"

// CORSConfig controls CORS middleware behavior.
type CORSConfig struct {
	Origins      []string // required; ["*"] = allow all
	MaxAge       int      // preflight Max-Age in seconds; 0 = omit
	AllowHeaders []string // nil = default (Content-Type, Authorization, X-Request-ID)
}

// CORS returns middleware that handles Cross-Origin Resource Sharing.
// Pass ["*"] in Origins to allow all origins. Preflight OPTIONS requests get a 204.
func CORS(cfg CORSConfig) Middleware {
	allow := make(map[string]struct{}, len(cfg.Origins))
	wildcard := false
	for _, o := range cfg.Origins {
		if o == "*" {
			wildcard = true
		}
		allow[o] = struct{}{}
	}
	headers := defaultAllowHeaders
	if len(cfg.AllowHeaders) > 0 {
		headers = strings.Join(cfg.AllowHeaders, ", ")
	}
	maxAge := ""
	if cfg.MaxAge > 0 {
		maxAge = strconv.Itoa(cfg.MaxAge)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !matchOrigin(origin, wildcard, allow) {
				next.ServeHTTP(w, r)
				return
			}
			setCORSHeaders(w, origin, wildcard, headers, maxAge)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchOrigin(origin string, wildcard bool, allow map[string]struct{}) bool {
	if wildcard {
		return true
	}
	_, ok := allow[origin]
	return ok
}

func setCORSHeaders(w http.ResponseWriter, origin string, wildcard bool, headers, maxAge string) {
	if wildcard {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", headers)
	if maxAge != "" {
		w.Header().Set("Access-Control-Max-Age", maxAge)
	}
}

func generateID() string {
	b := make([]byte, idBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
