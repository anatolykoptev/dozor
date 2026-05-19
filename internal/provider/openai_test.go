package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// apiToolCall is a test-only fixture for building tool_calls JSON in
// the OpenAI/Anthropic-compatible wire shape. Production uses
// kitllm.ToolCall; tests construct JSON directly so they exercise
// kitllm.Client's decode path end-to-end.
type apiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatCompletion builds a minimal valid OpenAI chat completion JSON response.
func chatCompletion(content, finishReason string) []byte {
	resp := map[string]any{
		"choices": []map[string]any{
			{
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": finishReason,
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// chatCompletionWithTools builds a chat completion JSON response that includes tool_calls.
func chatCompletionWithTools(content string, calls []apiToolCall) []byte {
	msg := map[string]any{"role": "assistant", "content": content, "tool_calls": calls}
	resp := map[string]any{
		"choices": []map[string]any{
			{
				"message":       msg,
				"finish_reason": "tool_calls",
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// openaiErrorBody returns an OpenAI-format error JSON body.
func openaiErrorBody(msg string) []byte {
	return []byte(fmt.Sprintf(`{"error":{"message":%q}}`, msg))
}

// newTestOpenAI constructs an OpenAI pointing at the given base URL with a
// short-timeout HTTP client. Created directly (same package) to avoid env var
// side effects and to allow injecting the test server URL.
func newTestOpenAI(baseURL string) *OpenAI {
	return &OpenAI{
		apiURL:   baseURL,
		apiKey:   "test-key",
		model:    "test-model",
		maxIters: 10,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// --- TestChat_Success -------------------------------------------------------

// TestChat_Success verifies that a well-formed 200 response is parsed into a
// Response with the expected Content and FinishReason.
func TestChat_Success(t *testing.T) {
	const wantContent = "Hello from the LLM!"
	const wantFinish = "stop"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletion(wantContent, wantFinish))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	msgs := []kitllm.Message{{Role: "user", Content: "ping"}}

	resp, err := p.Chat(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Chat() unexpected error: %v", err)
	}
	if resp.Content != wantContent {
		t.Errorf("Content = %q, want %q", resp.Content, wantContent)
	}
	if resp.FinishReason != wantFinish {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, wantFinish)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want empty", resp.ToolCalls)
	}
}

// --- TestChat_WithToolCalls -------------------------------------------------

// TestChat_WithToolCalls verifies that tool_calls in the API response are
// correctly parsed into Response.ToolCalls including the pre-parsed Args map.
func TestChat_WithToolCalls(t *testing.T) {
	calls := []apiToolCall{
		{
			ID:   "call-abc",
			Type: "function",
			Function: apiFunction{
				Name:      "get_server_status",
				Arguments: `{"host":"prod-1","port":22}`,
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletionWithTools("", calls))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	resp, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "check status"}}, nil)
	if err != nil {
		t.Fatalf("Chat() unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}

	tc := resp.ToolCalls[0]
	if tc.ID != "call-abc" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call-abc")
	}
	if tc.Type != "function" {
		t.Errorf("ToolCall.Type = %q, want %q", tc.Type, "function")
	}
	if tc.Function.Name != "get_server_status" {
		t.Errorf("Function.Name = %q, want %q", tc.Function.Name, "get_server_status")
	}
	if tc.Function.Arguments != `{"host":"prod-1","port":22}` {
		t.Errorf("Function.Arguments = %q, want raw JSON", tc.Function.Arguments)
	}

	// Verify args can be parsed from the raw JSON string.
	var parsedArgs map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsedArgs); err != nil {
		t.Fatalf("failed to parse Function.Arguments: %v", err)
	}
	if host, _ := parsedArgs["host"].(string); host != "prod-1" {
		t.Errorf("parsed args host = %q, want %q", host, "prod-1")
	}
}

// --- TestChat_AuthError -----------------------------------------------------

// TestChat_AuthError verifies that a 401 response is returned immediately as a
// kitllm.APIError without any retry attempts.
func TestChat_AuthError(t *testing.T) {
	var hitCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(openaiErrorBody("invalid api key"))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	_, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error, got nil")
	}

	var ae *kitllm.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error type = %T, want *kitllm.APIError", err)
	}
	if ae.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", ae.StatusCode)
	}
	if !IsAuth(err) {
		t.Error("IsAuth() = false, want true")
	}

	// Auth errors must not be retried — exactly one HTTP hit.
	if n := hitCount.Load(); n != 1 {
		t.Errorf("server hit count = %d, want 1 (no retries on auth error)", n)
	}
}

// --- TestChat_RateLimitRetry ------------------------------------------------

// TestChat_RateLimitRetry verifies that a single 429 response causes one retry
// and the final 200 is returned successfully.
//
// NOTE: This test incurs ~2 s of sleep (chatInitialDelay) due to the retry
// backoff. That is intentional — it validates the real retry path end-to-end.
func TestChat_RateLimitRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry sleep test in short mode")
	}

	var hitCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hitCount.Add(1)
		if n == 1 {
			// First request: rate limited.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write(openaiErrorBody("rate limit exceeded"))
			return
		}
		// Second request: success.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletion("retried successfully", "stop"))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	resp, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatalf("Chat() unexpected error after retry: %v", err)
	}
	if resp.Content != "retried successfully" {
		t.Errorf("Content = %q, want %q", resp.Content, "retried successfully")
	}
	if n := hitCount.Load(); n != 2 {
		t.Errorf("server hit count = %d, want 2 (initial + 1 retry)", n)
	}
}

// --- TestChat_ServerErrorRetry ----------------------------------------------

// TestChat_ServerErrorRetry verifies that two consecutive 500 responses cause
// two retries, and the final 200 is returned.
//
// NOTE: This test incurs ~6 s of sleep (2s + 4s backoff). Use -short to skip.
func TestChat_ServerErrorRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry sleep test in short mode")
	}

	var hitCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hitCount.Add(1)
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(openaiErrorBody("internal server error"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletion("recovered after retries", "stop"))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	resp, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "test"}}, nil)
	if err != nil {
		t.Fatalf("Chat() unexpected error after retries: %v", err)
	}
	if resp.Content != "recovered after retries" {
		t.Errorf("Content = %q, want %q", resp.Content, "recovered after retries")
	}
	if n := hitCount.Load(); n != 3 {
		t.Errorf("server hit count = %d, want 3 (initial + 2 retries)", n)
	}
}

// --- TestChat_MaxRetriesExhausted -------------------------------------------

// TestChat_MaxRetriesExhausted verifies that when the server always returns
// 500, Chat returns an error after exactly 4 attempts (1 initial + 3 retries)
// and the returned error is a kitllm.APIError with IsServerError() == true.
//
// NOTE: incurs up to ~14 s of sleep. Use -short to skip.
func TestChat_MaxRetriesExhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry sleep test in short mode")
	}

	var hitCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(openaiErrorBody("always failing"))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	_, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "test"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error when server always returns 500, got nil")
	}

	var ae *kitllm.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error type = %T, want *kitllm.APIError", err)
	}
	if !IsServerError(err) {
		t.Errorf("IsServerError() = false for status %d", ae.StatusCode)
	}

	// 4 total attempts: 1 initial + 3 retries (chatRetryOpts.MaxAttempts = 4).
	const wantHits = 4
	if n := hitCount.Load(); n != wantHits {
		t.Errorf("server hit count = %d, want %d (initial + 3 retries)", n, wantHits)
	}
}

// --- TestChat_NetworkError --------------------------------------------------

// TestChat_NetworkError verifies that when the server is unavailable (closed
// before the request), Chat returns a non-nil error.
// Retry sleep is not incurred because the server is closed synchronously and
// we only verify the final error without asserting hit count.
func TestChat_NetworkError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network retry sleep test in short mode")
	}

	// Start a server and immediately close it so the port is unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // closed before any request is made

	p := newTestOpenAI(addr)
	_, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "ping"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected network error, got nil")
	}

	// Network errors are NOT wrapped as kitllm.APIError.
	var ae *kitllm.APIError
	if errors.As(err, &ae) {
		t.Errorf("network error should not be a kitllm.APIError, got %T", err)
	}
}

// --- TestChat_EmptyChoices --------------------------------------------------

// TestChat_EmptyChoices verifies that an empty choices array in the response
// causes Chat to return a descriptive error.
//
// NOTE: empty-choices errors are not *kitllm.APIError; IsTransient returns false
// for them, so Chat returns immediately without retrying. Use -short to skip.
func TestChat_EmptyChoices(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry sleep test in short mode")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	_, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error for empty choices, got nil")
	}
}

// --- TestChat_BlockedResponse -----------------------------------------------

// TestChat_BlockedResponse verifies that a response with promptFeedback
// blockReason returns a descriptive error.
//
// NOTE: blocked-response errors are not *kitllm.APIError; IsTransient returns
// false for them, so Chat returns immediately without retrying. Use -short to skip.
func TestChat_BlockedResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry sleep test in short mode")
	}
	body := `{"choices":[],"promptFeedback":{"blockReason":"SAFETY"}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newTestOpenAI(srv.URL)
	_, err := p.Chat(context.Background(), []kitllm.Message{{Role: "user", Content: "evil prompt"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error for blocked response, got nil")
	}
}

