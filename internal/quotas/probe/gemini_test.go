package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiProbe_OKWithRateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1000")
		w.Header().Set("X-RateLimit-Remaining", "750")
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"models": []map[string]any{{"name": "gemini-pro"}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: "testkey", client: newTestClient(srv), baseURL: srv.URL}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("expected 1 reading, got %d", len(readings))
	}
	if readings[0].Product != "api_requests" {
		t.Errorf("expected product=api_requests, got %s", readings[0].Product)
	}
	// 750/1000 * 100 = 75%
	if readings[0].Remaining != 75 {
		t.Errorf("expected 75%% remaining, got %.2f", readings[0].Remaining)
	}
}

func TestGeminiProbe_OKNoRateLimitHeaders(t *testing.T) {
	// Paid tier: no rate-limit headers → report 100%.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"models": []map[string]any{{"name": "gemini-pro"}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: "testkey", client: newTestClient(srv), baseURL: srv.URL}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if readings[0].Remaining != 100 {
		t.Errorf("expected 100%% when no rate-limit headers, got %.2f", readings[0].Remaining)
	}
}

func TestGeminiProbe_RateLimitExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: "testkey", client: newTestClient(srv), baseURL: srv.URL}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if readings[0].Remaining != 0 {
		t.Errorf("expected 0%% remaining on 429, got %.2f", readings[0].Remaining)
	}
}

func TestGeminiProbe_AuthFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: "badkey", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsAuthFail(err) {
		t.Errorf("expected auth_fail error, got %T: %v", err, err)
	}
}

func TestGeminiProbe_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsHTTPErr(err) {
		t.Errorf("expected http_err, got %T: %v", err, err)
	}
}

func TestGeminiProbe_ParseErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsParseErr(err) {
		t.Errorf("expected parse_err, got %T: %v", err, err)
	}
}
