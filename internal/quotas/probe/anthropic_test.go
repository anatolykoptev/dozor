package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicTestServer sets up an httptest.Server that serves both the
// /v1/organizations endpoint (spending limit) and /v1/organizations/*/usage/messages
// endpoint (monthly token usage).
func anthropicTestServer(orgResp, usageResp any, orgStatus, usageStatus int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/usage/") {
			w.WriteHeader(usageStatus)
			if usageResp != nil {
				_ = json.NewEncoder(w).Encode(usageResp)
			}
			return
		}
		w.WriteHeader(orgStatus)
		if orgResp != nil {
			_ = json.NewEncoder(w).Encode(orgResp)
		}
	}))
}

func TestAnthropicProbe_OK(t *testing.T) {
	orgBody := map[string]any{"monthly_spending_limit_usd": 100.0}
	// 1M input tokens + 1M output tokens ≈ $3 + $15 = $18 → 82% remaining
	usageBody := map[string]any{
		"data": []map[string]any{
			{"input_tokens": 1_000_000, "output_tokens": 1_000_000, "cache_read_input_tokens": 0},
		},
	}
	srv := anthropicTestServer(orgBody, usageBody, http.StatusOK, http.StatusOK)
	defer srv.Close()

	p := &AnthropicProber{
		adminKey: "key",
		orgID:    "test-org",
		client:   newTestClient(srv),
		baseURL:  srv.URL,
	}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("expected 1 reading, got %d", len(readings))
	}
	if readings[0].Product != "spend" {
		t.Errorf("expected product=spend, got %s", readings[0].Product)
	}
	// (100 - 18) / 100 * 100 = 82
	if readings[0].Remaining != 82 {
		t.Errorf("expected 82%% remaining, got %.2f", readings[0].Remaining)
	}
}

func TestAnthropicProbe_NoSpendingLimit(t *testing.T) {
	orgBody := map[string]any{"monthly_spending_limit_usd": 0.0}
	srv := anthropicTestServer(orgBody, nil, http.StatusOK, http.StatusOK)
	defer srv.Close()

	p := &AnthropicProber{
		adminKey: "key",
		orgID:    "test-org",
		client:   newTestClient(srv),
		baseURL:  srv.URL,
	}
	_, err := p.Probe(context.Background())
	if !IsParseErr(err) {
		t.Errorf("expected parse_err for no spending limit, got %T: %v", err, err)
	}
}

func TestAnthropicProbe_AuthFail(t *testing.T) {
	srv := anthropicTestServer(nil, nil, http.StatusUnauthorized, http.StatusUnauthorized)
	defer srv.Close()

	p := &AnthropicProber{
		adminKey: "badkey",
		orgID:    "test-org",
		client:   newTestClient(srv),
		baseURL:  srv.URL,
	}
	_, err := p.Probe(context.Background())
	if !IsAuthFail(err) {
		t.Errorf("expected auth_fail error, got %T: %v", err, err)
	}
}

func TestAnthropicProbe_UsageNotFound(t *testing.T) {
	orgBody := map[string]any{"monthly_spending_limit_usd": 100.0}
	// Usage endpoint 404 → treated as 0 spend, not an error.
	srv := anthropicTestServer(orgBody, nil, http.StatusOK, http.StatusNotFound)
	defer srv.Close()

	p := &AnthropicProber{
		adminKey: "key",
		orgID:    "test-org",
		client:   newTestClient(srv),
		baseURL:  srv.URL,
	}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No usage data → 100% remaining.
	if readings[0].Remaining != 100 {
		t.Errorf("expected 100%% when usage 404, got %.2f", readings[0].Remaining)
	}
}

func TestAnthropicProbe_OrgServerError(t *testing.T) {
	srv := anthropicTestServer(nil, nil, http.StatusInternalServerError, http.StatusOK)
	defer srv.Close()

	p := &AnthropicProber{
		adminKey: "key",
		orgID:    "test-org",
		client:   newTestClient(srv),
		baseURL:  srv.URL,
	}
	_, err := p.Probe(context.Background())
	if !IsHTTPErr(err) {
		t.Errorf("expected http_err, got %T: %v", err, err)
	}
}
