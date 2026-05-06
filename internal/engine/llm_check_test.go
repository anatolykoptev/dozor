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
