package engine

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeGeminiKey = "AIzaSyFAKEfakeFAKEfakeFAKEfakeFAKE12345"

// captureSlog routes the default slog logger into a buffer for the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// assertAlertClean fails if any alert field, or the captured log, carries the
// key, a URL, or the upstream host. Alert.Description flows into the
// watch-triage LLM prompt and a Telegram report.
func assertAlertClean(t *testing.T, a *Alert, logs string) {
	t.Helper()
	if a == nil {
		t.Fatal("expected an alert")
	}
	fields := map[string]string{
		"Service": a.Service, "Title": a.Title, "Description": a.Description,
		"SuggestedAction": a.SuggestedAction, "logs": logs,
	}
	for name, text := range fields {
		for _, needle := range []string{fakeGeminiKey, "googleapis.com", "http://", "https://"} {
			if strings.Contains(text, needle) {
				t.Errorf("%s leaks %q: %s", name, needle, text)
			}
		}
	}
}

// onlyGeminiRewrite sends the Gemini host to target and passes everything else through.
type onlyGeminiRewrite struct{ target string }

func (r onlyGeminiRewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "generativelanguage.googleapis.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	return (&hostRewriteTransport{base: http.DefaultTransport, target: r.target}).RoundTrip(req)
}

// Control: the pre-fix request shape (?key= in the URL) puts the key into the
// transport error text. Without this the guards below could pass vacuously.
func TestCheckGeminiKey_Control_QueryKeyLeaksIntoTransportError(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://dozor-host-does-not-exist.invalid/v1beta/models?key="+fakeGeminiKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, doErr := client.Do(req)
	if doErr == nil || !strings.Contains(doErr.Error(), fakeGeminiKey) {
		t.Fatalf("control broken: old path no longer leaks, guard tests prove nothing: %v", doErr)
	}
	if strings.Contains(stripURLFromErr(doErr), fakeGeminiKey) {
		t.Fatal("stripURLFromErr must remove the URL")
	}
}

func TestCheckGeminiKey_UnresolvableHost_AlertHasNoKeyOrURL(t *testing.T) {
	logs := captureSlog(t)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &hostRewriteTransport{base: http.DefaultTransport, target: "http://dozor-host-does-not-exist.invalid"},
	}
	a := checkGeminiKey(t.Context(), client, fakeGeminiKey)
	assertAlertClean(t, a, logs.String())
	if a.Title != "unreachable" {
		t.Errorf("title = %q, want unreachable", a.Title)
	}
}

func TestCheckGeminiKey_HangingServer_AlertHasNoKeyOrURL(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	logs := captureSlog(t)
	client := &http.Client{
		Timeout:   150 * time.Millisecond,
		Transport: &hostRewriteTransport{base: http.DefaultTransport, target: srv.URL},
	}
	a := checkGeminiKey(t.Context(), client, fakeGeminiKey)
	assertAlertClean(t, a, logs.String())
	if strings.Contains(a.Description, "127.0.0.1") {
		t.Errorf("description leaks upstream address: %s", a.Description)
	}
}

func TestCheckGeminiKey_KeyInHeaderNotInURL(t *testing.T) {
	var gotHeader, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-goog-api-key")
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &hostRewriteTransport{base: http.DefaultTransport, target: srv.URL}}
	if a := checkGeminiKey(t.Context(), client, fakeGeminiKey); a != nil {
		t.Fatalf("expected nil alert, got %+v", a)
	}
	if gotHeader != fakeGeminiKey {
		t.Errorf("x-goog-api-key header = %q, want the key", gotHeader)
	}
	if gotQuery != "" {
		t.Errorf("URL query must be empty, got %q", gotQuery)
	}
}

func TestCheckGeminiKey_RedirectNotFollowed_HeaderNotForwarded(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	logs := captureSlog(t)
	// Same shape as production: newHTTPClient follows up to 5 redirects.
	client := newHTTPClient(5 * time.Second)
	client.Transport = onlyGeminiRewrite{target: redirector.URL}
	a := checkGeminiKey(t.Context(), client, fakeGeminiKey)
	if hits.Load() != 0 {
		t.Fatal("redirect was followed; x-goog-api-key forwarded to another host")
	}
	if a == nil {
		t.Fatal("expected an alert for the 307 response")
	}
	assertAlertClean(t, a, logs.String())
}
