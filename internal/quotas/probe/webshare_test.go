package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebshareProbe_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token testkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		resp := map[string]any{
			"bandwidth_gb": map[string]float64{
				"allowed": 100.0,
				"used":    25.0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &WebshareProber{
		apiKey:  "testkey",
		client:  newTestClient(srv),
		baseURL: srv.URL,
	}

	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("expected 1 reading, got %d", len(readings))
	}
	if readings[0].Product != "bandwidth" {
		t.Errorf("expected product=bandwidth, got %s", readings[0].Product)
	}
	// 75% remaining (100 - 25) / 100 * 100 = 75
	if readings[0].Remaining != 75 {
		t.Errorf("expected 75%% remaining, got %.2f", readings[0].Remaining)
	}
}

func TestWebshareProbe_AuthFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := &WebshareProber{apiKey: "badkey", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsAuthFail(err) {
		t.Errorf("expected auth_fail error, got %T: %v", err, err)
	}
}

func TestWebshareProbe_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := &WebshareProber{apiKey: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsHTTPErr(err) {
		t.Errorf("expected http_err, got %T: %v", err, err)
	}
}

func TestWebshareProbe_ParseErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	p := &WebshareProber{apiKey: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsParseErr(err) {
		t.Errorf("expected parse_err, got %T: %v", err, err)
	}
}

func TestWebshareProbe_ZeroLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{"bandwidth_gb": map[string]float64{"allowed": 0, "used": 0}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &WebshareProber{apiKey: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsParseErr(err) {
		t.Errorf("expected parse_err for zero limit, got %T: %v", err, err)
	}
}

// newTestClient returns an *http.Client that routes all requests to srv.
func newTestClient(srv *httptest.Server) *http.Client {
	return srv.Client()
}
