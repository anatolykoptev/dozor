package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// TestCooldownDuration_EnvOverride_BehavioralProof drives buildChainClient via
// NewOpenAI with LLM_COOLDOWN_SECONDS=2, trips cooldown, waits 2.1s, then
// asserts primary is retried — proving the env value reached kit.
//
// Falsifiability: revert to CooldownConfig{} (Default=0 → kit fallback=60s).
// At 2.1s the primary is still cooled → primaryHits after wait == 0 → FAIL.
func TestCooldownDuration_EnvOverride_BehavioralProof(t *testing.T) {
	t.Setenv("LLM_COOLDOWN_SECONDS", "2")

	var primaryHits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		model, _ := req["model"].(string)
		if model == "env-primary" {
			primaryHits.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	t.Setenv("DOZOR_LLM_URL", srv.URL)
	t.Setenv("DOZOR_LLM_API_KEY", "k")
	t.Setenv("DOZOR_LLM_MODEL", "env-primary")
	t.Setenv("DOZOR_LLM_MODEL_FALLBACK", "env-fallback")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("expected provider configured")
	}

	msgs := []kitllm.Message{{Role: "user", Content: "p"}}
	ctx := context.Background()

	// Trip cooldown (FailThreshold=2).
	for range 2 {
		_, _ = p.Chat(ctx, msgs, nil)
	}
	time.Sleep(10 * time.Millisecond)

	// During cooldown: primary must be skipped.
	primaryHits.Store(0)
	_, _ = p.Chat(ctx, msgs, nil)
	if primaryHits.Load() != 0 {
		t.Fatal("primary hit during cooldown — cooldown not active")
	}

	// Wait for 2s TTL to expire.
	time.Sleep(2100 * time.Millisecond)

	// After expiry: primary must be retried.
	primaryHits.Store(0)
	_, _ = p.Chat(ctx, msgs, nil)
	if primaryHits.Load() == 0 {
		t.Error("primary not retried after 2.1s — LLM_COOLDOWN_SECONDS did not reach kit (still cooled means Default > 2s)")
	}
}

// TestCooldownDuration_Default asserts the helper returns 5m when env is unset.
func TestCooldownDuration_Default(t *testing.T) {
	t.Setenv("LLM_COOLDOWN_SECONDS", "")
	got := cooldownDuration()
	if got != 5*time.Minute {
		t.Errorf("cooldownDuration() default = %v, want 5m", got)
	}
}

// TestNewOpenAI_ParsesChainFromEnv verifies that DOZOR_LLM_MODEL_FALLBACK
// is read and parsed into the OpenAI struct chain. Primary model stripped.
func TestNewOpenAI_ParsesChainFromEnv(t *testing.T) {
	t.Setenv("DOZOR_LLM_URL", "http://x/v1")
	t.Setenv("DOZOR_LLM_API_KEY", "k")
	t.Setenv("DOZOR_LLM_MODEL", "primary")
	t.Setenv("DOZOR_LLM_MODEL_FALLBACK", "primary,fb1,fb2,fb1")
	t.Setenv("LLM_MODEL_FALLBACK", "should-be-ignored")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("expected provider configured")
	}
	o := p.(*OpenAI)
	if len(o.fallbackChain) != 2 {
		t.Fatalf("expected 2 chain models (primary stripped, fb1 deduped), got %v", o.fallbackChain)
	}
	if o.fallbackChain[0] != "fb1" || o.fallbackChain[1] != "fb2" {
		t.Errorf("unexpected order %v", o.fallbackChain)
	}
}

// TestNewOpenAI_FallsBackToFleetEnv verifies fleet-wide LLM_MODEL_FALLBACK
// is read when DOZOR_LLM_MODEL_FALLBACK absent.
func TestNewOpenAI_FallsBackToFleetEnv(t *testing.T) {
	t.Setenv("DOZOR_LLM_URL", "http://x/v1")
	t.Setenv("DOZOR_LLM_API_KEY", "k")
	t.Setenv("DOZOR_LLM_MODEL", "primary")
	os.Unsetenv("DOZOR_LLM_MODEL_FALLBACK")
	t.Setenv("LLM_MODEL_FALLBACK", "fleet-a,fleet-b")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("expected provider configured")
	}
	o := p.(*OpenAI)
	if len(o.fallbackChain) != 2 {
		t.Fatalf("expected 2 chain models from fleet env, got %v", o.fallbackChain)
	}
	if o.fallbackChain[0] != "fleet-a" {
		t.Errorf("first model = %q, want fleet-a", o.fallbackChain[0])
	}
}

// TestOpenAI_ChainCascadesOn5xx exercises end-to-end Chat with chain —
// primary 503, fallback model returns 200, total 2 server hits.
func TestOpenAI_ChainCascadesOn5xx(t *testing.T) {
	var primaryHits, fbHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		model, _ := req["model"].(string)
		if model == "primary-broken" {
			primaryHits.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fbHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": "ok from fb"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	t.Setenv("DOZOR_LLM_URL", srv.URL)
	t.Setenv("DOZOR_LLM_API_KEY", "k")
	t.Setenv("DOZOR_LLM_MODEL", "primary-broken")
	t.Setenv("DOZOR_LLM_MODEL_FALLBACK", "fb-good")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("expected provider configured")
	}
	resp, err := p.Chat(context.Background(),
		[]kitllm.Message{{Role: "user", Content: "hi"}},
		nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok from fb" {
		t.Errorf("content = %q, want ok from fb", resp.Content)
	}
	if primaryHits.Load() == 0 {
		t.Error("primary model never hit")
	}
	if fbHits.Load() == 0 {
		t.Error("fallback model never hit")
	}
}
