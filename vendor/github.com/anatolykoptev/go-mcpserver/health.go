package mcpserver

import (
	"encoding/json"
	"net/http"
)

// healthInfo is the JSON shape returned from /health.
// Defined as a typed struct (vs map[string]string) so json.Marshal cannot
// fail at runtime — eliminating the silently-discarded error in registerHealth.
type healthInfo struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

// readyInfo is the JSON shape returned from /health/ready when the
// configured readiness check fails.
type readyInfo struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// registerHealth adds /health, /health/live, and /health/ready endpoints.
// If cfg.DisableHealth is true, no endpoints are registered.
func registerHealth(mux *http.ServeMux, cfg Config) {
	if cfg.DisableHealth {
		return
	}

	healthBody, err := json.Marshal(healthInfo{
		Status:  "ok",
		Service: cfg.Name,
		Version: cfg.Version,
	})
	if err != nil {
		// Marshalling a struct of three string fields cannot fail; this
		// branch only exists to satisfy errcheck and to defend against
		// future schema drift introducing a non-marshalable field.
		healthBody = []byte(`{"status":"ok"}`)
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(healthBody)
	})

	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if cfg.ReadinessCheck != nil {
			if err := cfg.ReadinessCheck(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				body, mErr := json.Marshal(readyInfo{
					Status: "unavailable",
					Error:  err.Error(),
				})
				if mErr != nil {
					body = []byte(`{"status":"unavailable"}`)
				}
				_, _ = w.Write(body)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}
