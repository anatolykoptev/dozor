package provider

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestFallback_NoSecondaryKey_NoExtraRTT(t *testing.T) {
	// Primary 500, fallback unset (key/url empty in env).
	// Expect: PR5 invariant — fallback is unavailable{} (never nil).
	// Calling fallback.Chat returns ErrUnavailable instantly.
	// hedge.DoFallback then returns the primary error (not ErrUnavailable).
	t.Setenv("DOZOR_LLM_URL", "http://example/v1")
	t.Setenv("DOZOR_LLM_MODEL", "test")
	t.Setenv("DOZOR_LLM_API_KEY", "k")
	os.Unsetenv("DOZOR_LLM_FALLBACK_URL")
	os.Unsetenv("DOZOR_LLM_FALLBACK_API_KEY")

	wf, ok := NewFromEnv()
	if !ok {
		t.Fatal("ok=false; DOZOR_LLM_API_KEY is set so primary should be available")
	}
	// PR5 invariant: fallback must never be nil.
	if wf.fallback == nil {
		t.Fatal("PR5 invariant violated: withFallback.fallback must never be nil (should be unavailable{})")
	}
	// Fallback must be unavailable{} when env is unset.
	if _, isUnavail := wf.fallback.(unavailable); !isUnavail {
		t.Fatalf("expected fallback=unavailable{} when env unset, got %T", wf.fallback)
	}
	// Calling fallback directly must return ErrUnavailable instantly (no RTT).
	_, err := wf.fallback.Chat(context.Background(), nil, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fallback.Chat: want ErrUnavailable, got %v", err)
	}
}

func TestFallback_WithFallbackEnv_NotUnavailable(t *testing.T) {
	// When fallback URL and key are set, fallback should be a real *OpenAI.
	t.Setenv("DOZOR_LLM_URL", "http://primary/v1")
	t.Setenv("DOZOR_LLM_MODEL", "primary-model")
	t.Setenv("DOZOR_LLM_API_KEY", "primary-key")
	t.Setenv("DOZOR_LLM_FALLBACK_URL", "http://fallback/v1")
	t.Setenv("DOZOR_LLM_FALLBACK_API_KEY", "fallback-key")

	wf, ok := NewFromEnv()
	if !ok {
		t.Fatal("ok=false; primary key is set")
	}
	if wf.fallback == nil {
		t.Fatal("fallback must never be nil")
	}
	if _, isUnavail := wf.fallback.(unavailable); isUnavail {
		t.Fatal("expected real fallback provider when env is set, got unavailable{}")
	}
}

func TestFallback_NoPrimaryKey_FallbackStillNonNil(t *testing.T) {
	// When primary key is missing, fallback still must not be nil.
	os.Unsetenv("DOZOR_LLM_API_KEY")
	os.Unsetenv("DOZOR_LLM_FALLBACK_URL")
	os.Unsetenv("DOZOR_LLM_FALLBACK_API_KEY")

	wf, ok := NewFromEnv()
	if ok {
		t.Fatal("ok=true; primary key is unset, expected false")
	}
	if wf.fallback == nil {
		t.Fatal("PR5 invariant violated: fallback must never be nil even when primary key is missing")
	}
}
