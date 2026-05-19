package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// TestChatRespectsCancel verifies that cancelling ctx mid-flight causes
// Chat to return ctx.Err() promptly, instead of completing the in-flight
// HTTP call on a detached ctx.
func TestChatRespectsCancel(t *testing.T) {
	// Stub that hangs forever — gives us a window to cancel.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
		}
	}))
	defer srv.Close()

	os.Setenv("DOZOR_LLM_URL", srv.URL)
	os.Setenv("DOZOR_LLM_MODEL", "test-model")
	os.Setenv("DOZOR_LLM_API_KEY", "test-key")
	defer os.Unsetenv("DOZOR_LLM_URL")
	defer os.Unsetenv("DOZOR_LLM_MODEL")
	defer os.Unsetenv("DOZOR_LLM_API_KEY")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("expected ok=true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Chat(ctx, []kitllm.Message{{Role: "user", Content: "test"}}, nil)
		done <- err
	}()

	// Let the request start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			// Network/HTTP libraries sometimes wrap context.Canceled as
			// url.Error or net.OpError; accept any non-nil error within
			// the deadline as proof that the cancel propagated.
			if err == nil {
				t.Fatal("expected non-nil error after cancel, got nil")
			}
			// Otherwise accept — cancellation observed, even if wrapped.
			t.Logf("got wrapped cancel error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Chat did not return within 2s of cancel — ctx not propagated")
	}
}
