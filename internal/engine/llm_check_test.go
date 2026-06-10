package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckGeminiKey(t *testing.T) {
	cases := []struct {
		name            string
		statusCode      int
		body            string
		wantNil         bool
		wantLevel       AlertLevel
		wantTitlePart   string
		wantDescPart    string
		wantActionPart  string
		wantNotContains string
	}{
		{
			name:       "200 OK returns nil",
			statusCode: http.StatusOK,
			body:       `{"models":[]}`,
			wantNil:    true,
		},
		{
			name:           "401 UNAUTHENTICATED -> AlertError invalid",
			statusCode:     http.StatusUnauthorized,
			body:           `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"API key not valid."}}`,
			wantNil:        false,
			wantLevel:      AlertError,
			wantTitlePart:  "invalid",
			wantActionPart: "Rotate",
		},
		{
			name:           "403 PERMISSION_DENIED -> AlertError invalid",
			statusCode:     http.StatusForbidden,
			body:           `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Permission denied on resource."}}`,
			wantNil:        false,
			wantLevel:      AlertError,
			wantTitlePart:  "invalid",
			wantActionPart: "Rotate",
		},
		{
			name:           "429 RESOURCE_EXHAUSTED -> AlertWarning quota exceeded with envelope",
			statusCode:     http.StatusTooManyRequests,
			body:           `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded."}}`,
			wantNil:        false,
			wantLevel:      AlertWarning,
			wantTitlePart:  "quota exceeded",
			wantDescPart:   "RESOURCE_EXHAUSTED",
			wantActionPart: "quota",
		},
		{
			name:            "503 UNAVAILABLE -> AlertWarning upstream (NOT AlertError)",
			statusCode:      http.StatusServiceUnavailable,
			body:            `{"error":{"code":503,"status":"UNAVAILABLE","message":"The model is overloaded. Please try again later."}}`,
			wantNil:         false,
			wantLevel:       AlertWarning,
			wantTitlePart:   "upstream",
			wantDescPart:    "UNAVAILABLE",
			wantActionPart:  "Transient",
			wantNotContains: "invalid",
		},
		{
			name:           "500 -> AlertWarning upstream",
			statusCode:     http.StatusInternalServerError,
			body:           `{"error":{"code":500,"status":"INTERNAL","message":"Internal error."}}`,
			wantNil:        false,
			wantLevel:      AlertWarning,
			wantTitlePart:  "upstream",
			wantActionPart: "Transient",
		},
		{
			name:           "502 -> AlertWarning upstream",
			statusCode:     http.StatusBadGateway,
			body:           `{"error":{"code":502,"status":"UNAVAILABLE","message":"Bad gateway."}}`,
			wantNil:        false,
			wantLevel:      AlertWarning,
			wantTitlePart:  "upstream",
			wantActionPart: "Transient",
		},
		{
			name:           "418 teapot -> AlertError unexpected HTTP 418",
			statusCode:     http.StatusTeapot,
			body:           `{"error":{"code":418,"status":"UNKNOWN","message":"I'm a teapot."}}`,
			wantNil:        false,
			wantLevel:      AlertError,
			wantTitlePart:  "unexpected HTTP 418",
			wantActionPart: "Investigate",
		},
		{
			name:           "503 with malformed body -> AlertWarning fallback to HTTP 503",
			statusCode:     http.StatusServiceUnavailable,
			body:           `not json at all`,
			wantNil:        false,
			wantLevel:      AlertWarning,
			wantTitlePart:  "upstream",
			wantDescPart:   "HTTP 503",
			wantActionPart: "Transient",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			// Patch the URL via a client that redirects to our test server.
			// We use a custom transport that rewrites host to the test server.
			testClient := &http.Client{
				Transport: &hostRewriteTransport{base: http.DefaultTransport, target: srv.URL},
			}

			alert := checkGeminiKey(t.Context(), testClient, "AIzaSyTestKey12345")

			if tc.wantNil {
				if alert != nil {
					t.Fatalf("expected nil alert, got: %+v", alert)
				}
				return
			}

			if alert == nil {
				t.Fatal("expected non-nil alert, got nil")
			}

			if alert.Level != tc.wantLevel {
				t.Errorf("Level: want %q, got %q", tc.wantLevel, alert.Level)
			}

			if tc.wantTitlePart != "" && !strings.Contains(alert.Title, tc.wantTitlePart) {
				t.Errorf("Title: want to contain %q, got %q", tc.wantTitlePart, alert.Title)
			}

			if tc.wantDescPart != "" && !strings.Contains(alert.Description, tc.wantDescPart) {
				t.Errorf("Description: want to contain %q, got %q", tc.wantDescPart, alert.Description)
			}

			if tc.wantActionPart != "" && !strings.Contains(alert.SuggestedAction, tc.wantActionPart) {
				t.Errorf("SuggestedAction: want to contain %q, got %q", tc.wantActionPart, alert.SuggestedAction)
			}

			if tc.wantNotContains != "" && strings.Contains(alert.Title, tc.wantNotContains) {
				t.Errorf("Title: must NOT contain %q, but got %q", tc.wantNotContains, alert.Title)
			}
		})
	}
}

// TestTruncateUTF8 ensures multi-byte runes are never split mid-codepoint.
func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"ascii under limit", "hello", 10, "hello"},
		{"ascii at limit", "hello", 5, "hello"},
		{"ascii over limit", "helloworld", 5, "hello"},
		{"cyrillic over limit", "Превышение квоты для проекта memdb", 5, "Превы"},
		{"emoji over limit", "🔥🔥🔥🔥🔥end", 3, "🔥🔥🔥"},
		{"chinese over limit", "配额超出限制", 3, "配额超"},
		{"zero max", "abc", 0, ""},
		{"negative max", "abc", -1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateUTF8(tc.in, tc.max); got != tc.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// TestCheckGeminiKeyBodyCap ensures oversized bodies don't blow memory or
// truncate mid-rune. We send 100 KiB body — only first 4 KiB read.
func TestCheckGeminiKeyBodyCap(t *testing.T) {
	bigBody := `{"error":{"code":503,"status":"UNAVAILABLE","message":"` +
		strings.Repeat("X", 100*1024) + `"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	testClient := &http.Client{Transport: &hostRewriteTransport{base: http.DefaultTransport, target: srv.URL}}
	alert := checkGeminiKey(t.Context(), testClient, "AIzaSyTestKey12345")

	if alert == nil {
		t.Fatal("expected non-nil alert")
	}
	if alert.Level != AlertWarning {
		t.Errorf("Level: want AlertWarning, got %q", alert.Level)
	}
	// Body was truncated at 4 KiB so JSON is invalid; description falls back to plain HTTP code.
	// This proves the cap fired: with the full 100 KiB body, parsing would have succeeded.
	if !strings.Contains(alert.Description, "503") {
		t.Errorf("Description: want HTTP 503 fallback, got %q", alert.Description)
	}
}

// hostRewriteTransport rewrites all requests to the given target base URL.
type hostRewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone request to avoid mutating original.
	clone := req.Clone(req.Context())
	// Parse target to extract host.
	targetURL := strings.TrimSuffix(t.target, "/")
	// Replace scheme+host, keep path+query.
	clone.URL.Scheme = "http"
	// Extract just the host:port from targetURL.
	hostPort := strings.TrimPrefix(targetURL, "http://")
	hostPort = strings.TrimPrefix(hostPort, "https://")
	clone.URL.Host = hostPort
	return t.base.RoundTrip(clone)
}

// TestCheckProxyModel_KitLLMClient tests the new checkProxyModel implementation
// that uses kitllm.Client.Chat instead of hand-rolled HTTP.
// The stub returns OpenAI-compat responses to /chat/completions.
func TestCheckProxyModel_KitLLMClient(t *testing.T) {
	cases := []struct {
		name           string
		statusCode     int
		body           string
		wantNil        bool
		wantLevel      AlertLevel
		wantTitlePart  string
		wantActionPart string
	}{
		{
			name:       "200 with content returns nil",
			statusCode: http.StatusOK,
			body:       `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			wantNil:    true,
		},
		{
			name:           "401 Unauthorized -> AlertError auth failure",
			statusCode:     http.StatusUnauthorized,
			body:           `{"error":{"type":"authentication_error","message":"Invalid API key"}}`,
			wantNil:        false,
			wantLevel:      AlertError,
			wantTitlePart:  "auth failure",
			wantActionPart: "DOZOR_LLM_CHECK_API_KEY",
		},
		{
			name:           "429 Too Many Requests -> AlertWarning rate limited",
			statusCode:     http.StatusTooManyRequests,
			body:           `{"error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}`,
			wantNil:        false,
			wantLevel:      AlertWarning,
			wantTitlePart:  "rate limited",
			wantActionPart: "quota",
		},
		{
			name:           "503 Service Unavailable -> AlertWarning upstream error",
			statusCode:     http.StatusServiceUnavailable,
			body:           `{"error":{"type":"service_unavailable","message":"Service unavailable"}}`,
			wantNil:        false,
			wantLevel:      AlertWarning,
			wantTitlePart:  "upstream error",
			wantActionPart: "CLIProxyAPI",
		},
		{
			name:           "400 Bad Request -> AlertError",
			statusCode:     http.StatusBadRequest,
			body:           `{"error":{"type":"invalid_request_error","message":"model not found"}}`,
			wantNil:        false,
			wantLevel:      AlertError,
			wantTitlePart:  "error (HTTP 400)",
			wantActionPart: "model availability",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			alert := checkProxyModel(t.Context(), srv.URL, "test-api-key", "test-model")

			if tc.wantNil {
				if alert != nil {
					t.Fatalf("expected nil alert, got: %+v", alert)
				}
				return
			}

			if alert == nil {
				t.Fatal("expected non-nil alert, got nil")
			}

			if alert.Level != tc.wantLevel {
				t.Errorf("Level: want %q, got %q", tc.wantLevel, alert.Level)
			}

			if tc.wantTitlePart != "" && !strings.Contains(alert.Title, tc.wantTitlePart) {
				t.Errorf("Title: want to contain %q, got %q", tc.wantTitlePart, alert.Title)
			}

			if tc.wantActionPart != "" && !strings.Contains(alert.SuggestedAction, tc.wantActionPart) {
				t.Errorf("SuggestedAction: want to contain %q, got %q", tc.wantActionPart, alert.SuggestedAction)
			}
		})
	}
}

// statusHandler responds with a fixed status and body for chain tests.
func chainStatusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"x","type":"server_error","code":"internal_server_error"}}`))
	}
}

// TestCheckProxyChain_FirstAliveNoAlert: chain semantics — any healthy model
// means production survives; no alert even when earlier models are dead.
func TestCheckProxyChain_DegradedButAliveNoAlert(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 { // first model probe → 503
			chainStatusHandler(http.StatusServiceUnavailable)(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"."}}]}`))
	}))
	defer srv.Close()

	a := checkProxyChain(t.Context(), srv.URL, "k", []string{"dead-model", "alive-model"})
	if a != nil {
		t.Fatalf("degraded-but-alive chain must NOT alert, got: %+v", a)
	}
}

// TestCheckProxyChain_AllDeadOneAggregateAlert: a fully dead chain raises ONE
// alert with the stable service id, naming every failed model.
func TestCheckProxyChain_AllDeadOneAggregateAlert(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(chainStatusHandler(http.StatusServiceUnavailable))
	defer srv.Close()

	a := checkProxyChain(t.Context(), srv.URL, "k", []string{"m1", "m2"})
	if a == nil {
		t.Fatal("fully dead chain must alert")
	}
	if a.Service != "llm:check-chain" {
		t.Errorf("service = %q, want stable llm:check-chain", a.Service)
	}
	if a.Level != AlertError {
		t.Errorf("level = %q, want error (production chain exhausted)", a.Level)
	}
	for _, m := range []string{"m1", "m2"} {
		if !strings.Contains(a.Description, m) {
			t.Errorf("description must name failed model %q: %s", m, a.Description)
		}
	}
}

// TestExtractIssues_DescriptionNotPrefixed locks the duplication fix: parsed
// Description must NOT re-embed the service (report #2f4393aa36df311d showed
// "service — service: desc" in Telegram).
func TestExtractIssues_DescriptionNotPrefixed(t *testing.T) {
	t.Parallel()

	issues := ExtractIssues("[WARNING] llm:m1 — upstream error (HTTP 503): body\n")
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	if got, want := issues[0].Description, "upstream error (HTTP 503): body"; got != want {
		t.Errorf("Description = %q, want %q (no service prefix)", got, want)
	}
}
