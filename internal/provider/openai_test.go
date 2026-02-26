package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// chatCompletion builds a minimal valid OpenAI chat completion JSON response.
func chatCompletion(content, finishReason string) []byte {
	resp := chatCompletionResponse{
		Choices: []chatChoice{
			{
				Message:      chatMessage{Role: "assistant", Content: content},
				FinishReason: finishReason,
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// chatCompletionWithTools builds a chat completion JSON response that includes tool_calls.
func chatCompletionWithTools(content string, calls []apiToolCall) []byte {
	resp := chatCompletionResponse{
		Choices: []chatChoice{
			{
				Message:      chatMessage{Role: "assistant", Content: content, ToolCalls: calls},
				FinishReason: "tool_calls",
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
	msgs := []Message{{Role: "user", Content: "ping"}}

	resp, err := p.Chat(msgs, nil)
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
	resp, err := p.Chat([]Message{{Role: "user", Content: "check status"}}, nil)
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
	if tc.Name != "get_server_status" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "get_server_status")
	}
	if tc.Function == nil {
		t.Fatal("ToolCall.Function is nil")
	}
	if tc.Function.Name != "get_server_status" {
		t.Errorf("Function.Name = %q, want %q", tc.Function.Name, "get_server_status")
	}
	if tc.Function.Arguments != `{"host":"prod-1","port":22}` {
		t.Errorf("Function.Arguments = %q, want raw JSON", tc.Function.Arguments)
	}

	// Verify pre-parsed args.
	if tc.Args == nil {
		t.Fatal("ToolCall.Args is nil — pre-parsing failed")
	}
	if host, _ := tc.Args["host"].(string); host != "prod-1" {
		t.Errorf("Args[host] = %q, want %q", host, "prod-1")
	}
}

// --- TestChat_AuthError -----------------------------------------------------

// TestChat_AuthError verifies that a 401 response is returned immediately as a
// ProviderError without any retry attempts.
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
	_, err := p.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error, got nil")
	}

	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error type = %T, want *ProviderError", err)
	}
	if pe.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", pe.StatusCode)
	}
	if !pe.IsAuth() {
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
	resp, err := p.Chat([]Message{{Role: "user", Content: "hello"}}, nil)
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
	resp, err := p.Chat([]Message{{Role: "user", Content: "test"}}, nil)
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
// 500, Chat returns an error after exactly chatMaxRetries+1 attempts and the
// returned error is a ProviderError with IsServerError() == true.
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
	_, err := p.Chat([]Message{{Role: "user", Content: "test"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error when server always returns 500, got nil")
	}

	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error type = %T, want *ProviderError", err)
	}
	if !pe.IsServerError() {
		t.Errorf("IsServerError() = false for status %d", pe.StatusCode)
	}

	// chatMaxRetries=3, so initial attempt + 3 retries = 4 total.
	const wantHits = chatMaxRetries + 1
	if n := hitCount.Load(); n != int32(wantHits) {
		t.Errorf("server hit count = %d, want %d (initial + %d retries)", n, wantHits, chatMaxRetries)
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
	_, err := p.Chat([]Message{{Role: "user", Content: "ping"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected network error, got nil")
	}

	// Network errors are NOT wrapped as ProviderError.
	var pe *ProviderError
	if errors.As(err, &pe) {
		t.Errorf("network error should not be a ProviderError, got %T", err)
	}
}

// --- TestChat_EmptyChoices --------------------------------------------------

// TestChat_EmptyChoices verifies that an empty choices array in the response
// causes Chat to return a descriptive error.
//
// NOTE: empty-choices errors are not *ProviderError, so chatWithRetry treats
// them as network errors and applies full retry backoff (~14 s). Use -short to
// skip.
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
	_, err := p.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error for empty choices, got nil")
	}
}

// --- TestChat_BlockedResponse -----------------------------------------------

// TestChat_BlockedResponse verifies that a response with promptFeedback
// blockReason returns a descriptive error.
//
// NOTE: blocked-response errors are not *ProviderError, so chatWithRetry
// applies full retry backoff (~14 s). Use -short to skip.
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
	_, err := p.Chat([]Message{{Role: "user", Content: "evil prompt"}}, nil)
	if err == nil {
		t.Fatal("Chat() expected error for blocked response, got nil")
	}
}

// --- TestShouldRetry_Classification -----------------------------------------

// TestShouldRetry_Classification unit-tests shouldRetry directly for all
// relevant error kinds and attempt values. No HTTP or sleep is involved.
func TestShouldRetry_Classification(t *testing.T) {
	authErr := &ProviderError{StatusCode: http.StatusUnauthorized, Message: "unauthorized"}
	forbiddenErr := &ProviderError{StatusCode: http.StatusForbidden, Message: "forbidden"}
	rateLimitErr := &ProviderError{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}
	rateLimitWithRetryAfter := &ProviderError{
		StatusCode: http.StatusTooManyRequests,
		Message:    "rate limited",
		RetryAfter: 5 * time.Second,
	}
	serverErr := &ProviderError{StatusCode: http.StatusInternalServerError, Message: "internal error"}
	badGatewayErr := &ProviderError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
	notFoundErr := &ProviderError{StatusCode: http.StatusNotFound, Message: "not found"}
	networkErr := errors.New("connection refused")

	cases := []struct {
		name        string
		err         error
		attempt     int
		wantRetry   bool
		wantNonZero bool // delay > 0
	}{
		// Auth errors — never retry regardless of attempt.
		{name: "auth_401_attempt0", err: authErr, attempt: 0, wantRetry: false},
		{name: "auth_401_attempt1", err: authErr, attempt: 1, wantRetry: false},
		{name: "auth_403_attempt0", err: forbiddenErr, attempt: 0, wantRetry: false},

		// 429 rate limit — retry while attempts remain, stop at chatMaxRetries.
		{name: "ratelimit_attempt0", err: rateLimitErr, attempt: 0, wantRetry: true, wantNonZero: true},
		{name: "ratelimit_attempt1", err: rateLimitErr, attempt: 1, wantRetry: true, wantNonZero: true},
		{name: "ratelimit_attempt2", err: rateLimitErr, attempt: 2, wantRetry: true, wantNonZero: true},
		{name: "ratelimit_atMaxRetries", err: rateLimitErr, attempt: chatMaxRetries, wantRetry: false},

		// 429 with Retry-After: delay should be at least RetryAfter value.
		{name: "ratelimit_retry_after_attempt0", err: rateLimitWithRetryAfter, attempt: 0, wantRetry: true, wantNonZero: true},

		// 5xx server errors — retry while attempts remain.
		{name: "server500_attempt0", err: serverErr, attempt: 0, wantRetry: true, wantNonZero: true},
		{name: "server502_attempt0", err: badGatewayErr, attempt: 0, wantRetry: true, wantNonZero: true},
		{name: "server500_atMaxRetries", err: serverErr, attempt: chatMaxRetries, wantRetry: false},

		// Non-transient provider errors (4xx non-auth) — do not retry.
		{name: "notfound_404_attempt0", err: notFoundErr, attempt: 0, wantRetry: false},

		// Network errors (non-ProviderError) — retry while attempts remain.
		{name: "network_attempt0", err: networkErr, attempt: 0, wantRetry: true, wantNonZero: true},
		{name: "network_attempt2", err: networkErr, attempt: 2, wantRetry: true, wantNonZero: true},
		{name: "network_atMaxRetries", err: networkErr, attempt: chatMaxRetries, wantRetry: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retry, delay := shouldRetry(tc.err, tc.attempt)
			if retry != tc.wantRetry {
				t.Errorf("shouldRetry(%T, %d) retry = %v, want %v", tc.err, tc.attempt, retry, tc.wantRetry)
			}
			if tc.wantNonZero && delay == 0 {
				t.Errorf("shouldRetry(%T, %d) delay = 0, want > 0", tc.err, tc.attempt)
			}
			if !tc.wantRetry && delay != 0 {
				t.Errorf("shouldRetry(%T, %d) delay = %v, want 0 when not retrying", tc.err, tc.attempt, delay)
			}
		})
	}
}

// TestShouldRetry_RateLimitRetryAfterDelay verifies that when RetryAfter is
// set on a rate-limit error, the returned delay is at least RetryAfter.
func TestShouldRetry_RateLimitRetryAfterDelay(t *testing.T) {
	const retryAfter = 5 * time.Second
	pe := &ProviderError{
		StatusCode: http.StatusTooManyRequests,
		RetryAfter: retryAfter,
	}
	retry, delay := shouldRetry(pe, 0)
	if !retry {
		t.Fatal("shouldRetry() = false, want true for 429 at attempt 0")
	}
	if delay < retryAfter {
		t.Errorf("delay = %v, want >= %v (RetryAfter respected)", delay, retryAfter)
	}
}

// --- TestProviderError_Methods ----------------------------------------------

// TestProviderError_Methods verifies the ProviderError helper methods cover
// all status code boundaries correctly.
func TestProviderError_Methods(t *testing.T) {
	cases := []struct {
		status      int
		wantAuth    bool
		wantRate    bool
		wantServer  bool
		wantTransient bool
	}{
		{http.StatusUnauthorized, true, false, false, false},
		{http.StatusForbidden, true, false, false, false},
		{http.StatusTooManyRequests, false, true, false, true},
		{http.StatusInternalServerError, false, false, true, true},
		{http.StatusBadGateway, false, false, true, true},
		{http.StatusServiceUnavailable, false, false, true, true},
		{http.StatusGatewayTimeout, false, false, true, true},
		{http.StatusNotFound, false, false, false, false},
		{http.StatusBadRequest, false, false, false, false},
	}

	for _, tc := range cases {
		pe := &ProviderError{StatusCode: tc.status}
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			if got := pe.IsAuth(); got != tc.wantAuth {
				t.Errorf("IsAuth() = %v, want %v for %d", got, tc.wantAuth, tc.status)
			}
			if got := pe.IsRateLimit(); got != tc.wantRate {
				t.Errorf("IsRateLimit() = %v, want %v for %d", got, tc.wantRate, tc.status)
			}
			if got := pe.IsServerError(); got != tc.wantServer {
				t.Errorf("IsServerError() = %v, want %v for %d", got, tc.wantServer, tc.status)
			}
			if got := pe.IsTransient(); got != tc.wantTransient {
				t.Errorf("IsTransient() = %v, want %v for %d", got, tc.wantTransient, tc.status)
			}
		})
	}
}

// --- TestParseProviderError -------------------------------------------------

// TestParseProviderError verifies that parseProviderError extracts the message
// from both OpenAI-format and plain-text error bodies.
func TestParseProviderError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       []byte
		wantMsg    string
	}{
		{
			name:       "openai_format",
			statusCode: http.StatusUnauthorized,
			body:       []byte(`{"error":{"message":"invalid api key"}}`),
			wantMsg:    "invalid api key",
		},
		{
			name:       "google_format",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"error":{"message":"quota exceeded","details":[{"metadata":{"retryDelay":"30s"}}]}}`),
			wantMsg:    "quota exceeded",
		},
		{
			name:       "plain_text_fallback",
			statusCode: http.StatusInternalServerError,
			body:       []byte("Internal Server Error\nsome extra details"),
			wantMsg:    "Internal Server Error",
		},
		{
			name:       "empty_body",
			statusCode: http.StatusBadGateway,
			body:       []byte(""),
			wantMsg:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := parseProviderError(tc.statusCode, tc.body)
			if pe.StatusCode != tc.statusCode {
				t.Errorf("StatusCode = %d, want %d", pe.StatusCode, tc.statusCode)
			}
			if pe.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", pe.Message, tc.wantMsg)
			}
		})
	}
}

// TestParseProviderError_GoogleRetryAfter verifies that the Retry-After is
// parsed from the Google error details metadata.
func TestParseProviderError_GoogleRetryAfter(t *testing.T) {
	body := []byte(`{"error":{"message":"quota exceeded","details":[{"metadata":{"retryDelay":"30s"}}]}}`)
	pe := parseProviderError(http.StatusTooManyRequests, body)
	if pe.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", pe.RetryAfter)
	}
}
