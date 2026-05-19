package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

func TestChatWithRetry_HonoursRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	os.Setenv("DOZOR_LLM_URL", srv.URL)
	os.Setenv("DOZOR_LLM_MODEL", "test")
	os.Setenv("DOZOR_LLM_API_KEY", "k")
	defer os.Unsetenv("DOZOR_LLM_URL")
	defer os.Unsetenv("DOZOR_LLM_MODEL")
	defer os.Unsetenv("DOZOR_LLM_API_KEY")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("ok=false")
	}

	start := time.Now()
	_, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "hi"}}, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected success after retry: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 retry), got %d", got)
	}
	// Server said Retry-After: 1 => expect ~1s elapsed. Default exponential
	// first delay is 2s (+jitter up to 2.5s). Upper bound <1.8s distinguishes
	// Retry-After path from the default exponential (2s+).
	if elapsed < 900*time.Millisecond {
		t.Errorf("elapsed %v, want >=1s (Retry-After honoured)", elapsed)
	}
	if elapsed > 1800*time.Millisecond {
		t.Errorf("elapsed %v, want <1.8s (Retry-After 1s, not default exponential 2s+)", elapsed)
	}
}
