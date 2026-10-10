package mcpserver

import (
	"context"
	"net/http"
)

// sdkPropagatesFrom is the first protocol version for which go-sdk's
// PropagateRequestCancellation cancels the handler context itself.
const sdkPropagatesFrom = "2026-07-28"

// clientCtxKey keys the originating HTTP request context in the handler ctx.
type clientCtxKey struct{}

// trackClientDisconnect wraps the MCP handler so tool calls can observe the
// HTTP request context. go-sdk passes req.Context() to the stateless session
// connection so middleware can add context VALUES (cancellation is suppressed),
// which is the channel used here. The SDK's own PropagateRequestCancellation
// only covers 2026-07-28 requests; this covers 2025-11-25 and older stateless
// POSTs (2026-07-28 requests are left to the SDK).
func trackClientDisconnect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Lexicographic compare of ISO dates (YYYY-MM-DD) is chronological and
		// is how the SDK itself orders protocol versions
		// (streamable_headers.go: header < minVersionForStandardHeaders).
		if r.Method != http.MethodPost || r.Header.Get("Mcp-Protocol-Version") >= sdkPropagatesFrom {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientCtxKey{}, r.Context())))
	})
}

// clientRequestCtx returns the originating HTTP request context stored by
// trackClientDisconnect, or nil for untracked requests.
func clientRequestCtx(ctx context.Context) context.Context {
	cc, _ := ctx.Value(clientCtxKey{}).(context.Context)
	return cc
}
