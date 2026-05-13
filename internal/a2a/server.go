package a2a

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// ErrSecretRequired is returned by Register when DOZOR_A2A_SECRET is empty and
// DOZOR_A2A_ALLOW_INSECURE is not set to "true". The A2A endpoint grants full
// tool palette access; running it without authentication is unsafe.
var ErrSecretRequired = errors.New("a2a: DOZOR_A2A_SECRET must be set; set DOZOR_A2A_ALLOW_INSECURE=true to override (dev only)")

// Register sets up A2A protocol routes on the given mux.
// Agent card is served publicly; /a2a endpoint requires Bearer auth.
//
// Fail-closed: if secret is empty and DOZOR_A2A_ALLOW_INSECURE != "true",
// Register returns ErrSecretRequired and does NOT register the /a2a endpoint.
// This prevents the 2026-05-12 auth-bypass: an empty secret previously allowed
// any localhost caller to execute claude_code with the full tool palette.
func Register(mux *http.ServeMux, proc MessageProcessor, registry *toolreg.Registry, baseURL, version, secret string) error {
	if secret == "" {
		if os.Getenv("DOZOR_A2A_ALLOW_INSECURE") != "true" {
			slog.Error("a2a endpoint disabled: DOZOR_A2A_SECRET is not set",
				slog.String("hint", "set DOZOR_A2A_SECRET or DOZOR_A2A_ALLOW_INSECURE=true (dev only)"))
			return ErrSecretRequired
		}
		slog.Warn("A2A endpoint unauthenticated — DOZOR_A2A_ALLOW_INSECURE override active; DO NOT use in production")
	}

	card := BuildAgentCard(baseURL, version, registry)

	executor := NewExecutor(proc)
	handler := a2asrv.NewHandler(executor)
	jsonrpcHandler := a2asrv.NewJSONRPCHandler(handler)

	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/a2a", bearerAuthMiddleware(jsonrpcHandler, secret))

	slog.Info("a2a protocol enabled",
		slog.String("card_url", baseURL+a2asrv.WellKnownAgentCardPath),
		slog.String("endpoint", baseURL+"/a2a"),
		slog.Int("skills", len(card.Skills)))

	return nil
}

// bearerAuthMiddleware enforces Bearer token authentication on the /a2a endpoint.
//
// Behavior matrix:
//   - secret != ""                           → enforce Bearer auth (production path)
//   - secret == "" && ALLOW_INSECURE != true → return 503 (fail-closed, defense-in-depth)
//   - secret == "" && ALLOW_INSECURE == true → pass-through with WARN log (dev opt-in)
func bearerAuthMiddleware(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret == "" {
			if os.Getenv("DOZOR_A2A_ALLOW_INSECURE") == "true" {
				slog.Warn("A2A request served unauthenticated — DOZOR_A2A_ALLOW_INSECURE override active",
					slog.String("remote", r.RemoteAddr))
				next.ServeHTTP(w, r)
				return
			}
			// Defense-in-depth: Register should have rejected this config, but if
			// middleware is reached with an empty secret and no opt-in, refuse.
			slog.Error("A2A endpoint refusing request: no secret configured and DOZOR_A2A_ALLOW_INSECURE not set")
			http.Error(w, "service unavailable: a2a endpoint not properly configured", http.StatusServiceUnavailable)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
				if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(secret)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
