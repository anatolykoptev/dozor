package a2a

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// Register sets up A2A protocol routes on the given mux.
// Agent card is served publicly; /a2a endpoint requires Bearer auth.
func Register(mux *http.ServeMux, proc MessageProcessor, registry *toolreg.Registry, baseURL, version, secret string) {
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
}

func bearerAuthMiddleware(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret == "" {
			next.ServeHTTP(w, r)
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
