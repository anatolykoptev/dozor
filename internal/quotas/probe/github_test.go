package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubProbe_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token testpat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		resp := map[string]any{
			"total_minutes_used":      600.0,
			"total_paid_minutes_used": 0.0,
			"included_minutes":        2000.0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &GitHubProber{pat: "testpat", client: newTestClient(srv), baseURL: srv.URL}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("expected 1 reading, got %d", len(readings))
	}
	if readings[0].Product != "actions_minutes" {
		t.Errorf("expected product=actions_minutes, got %s", readings[0].Product)
	}
	// (2000 - 600) / 2000 * 100 = 70
	if readings[0].Remaining != 70 {
		t.Errorf("expected 70%% remaining, got %.2f", readings[0].Remaining)
	}
}

func TestGitHubProbe_ZeroIncludedMinutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"total_minutes_used":      10.0,
			"total_paid_minutes_used": 10.0,
			"included_minutes":        0.0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &GitHubProber{pat: "key", client: newTestClient(srv), baseURL: srv.URL}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Zero included_minutes → safe default 100%.
	if readings[0].Remaining != 100 {
		t.Errorf("expected 100%% remaining for zero included_minutes, got %.2f", readings[0].Remaining)
	}
}

func TestGitHubProbe_AuthFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := &GitHubProber{pat: "badpat", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsAuthFail(err) {
		t.Errorf("expected auth_fail error, got %T: %v", err, err)
	}
}

func TestGitHubProbe_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := &GitHubProber{pat: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsHTTPErr(err) {
		t.Errorf("expected http_err, got %T: %v", err, err)
	}
}

func TestGitHubProbe_ParseErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	p := &GitHubProber{pat: "key", client: newTestClient(srv), baseURL: srv.URL}
	_, err := p.Probe(context.Background())
	if !IsParseErr(err) {
		t.Errorf("expected parse_err, got %T: %v", err, err)
	}
}

func TestGitHubProbe_ClampOveruse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"total_minutes_used": 3000.0,
			"included_minutes":   2000.0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &GitHubProber{pat: "key", client: newTestClient(srv), baseURL: srv.URL}
	readings, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Over-used: remaining clamped to 0.
	if readings[0].Remaining != 0 {
		t.Errorf("expected 0%% for over-used quota, got %.2f", readings[0].Remaining)
	}
}
