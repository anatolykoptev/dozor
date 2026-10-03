package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeProbeKey = "AIzaSyFAKEfakeFAKEfakeFAKEfakeFAKE12345"

func assertProbeErrClean(t *testing.T, err error, extra ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	// runner.runOne logs this error verbatim via slog.Any("err", err).
	for _, needle := range append([]string{fakeProbeKey, "http://", "https://", "key="}, extra...) {
		if strings.Contains(err.Error(), needle) {
			t.Errorf("probe error leaks %q: %v", needle, err)
		}
	}
}

// Control: the pre-fix request shape leaks the key into the transport error.
func TestGeminiProbe_Control_QueryKeyLeaksIntoTransportError(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://gemini-host-does-not-exist.invalid/v1beta/models?key="+fakeProbeKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, doErr := client.Do(req)
	if doErr == nil || !strings.Contains(doErr.Error(), fakeProbeKey) {
		t.Fatalf("control broken: old path no longer leaks, guard tests prove nothing: %v", doErr)
	}
}

func TestGeminiProbe_UnresolvableHost_ErrorHasNoKeyOrURL(t *testing.T) {
	p := &GeminiProber{apiKey: fakeProbeKey, client: &http.Client{Timeout: 5 * time.Second},
		baseURL: "http://gemini-host-does-not-exist.invalid"}
	_, err := p.Probe(context.Background())
	assertProbeErrClean(t, err)
	if !IsTimeout(err) {
		t.Errorf("transport failure must still classify as timeout/net, got %T", err)
	}
}

func TestGeminiProbe_HangingServer_ErrorHasNoKeyOrURL(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	p := &GeminiProber{apiKey: fakeProbeKey, client: &http.Client{}, baseURL: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := p.Probe(ctx)
	assertProbeErrClean(t, err, "127.0.0.1")
	if !IsTimeout(err) {
		t.Errorf("expected timeout classification, got %T", err)
	}
}

func TestGeminiProbe_KeyInHeaderNotInURL(t *testing.T) {
	var gotHeader, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-goog-api-key")
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	p := &GeminiProber{apiKey: fakeProbeKey, client: srv.Client(), baseURL: srv.URL}
	if _, err := p.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotHeader != fakeProbeKey {
		t.Errorf("x-goog-api-key header = %q, want the key", gotHeader)
	}
	if gotQuery != "" {
		t.Errorf("URL query must be empty, got %q", gotQuery)
	}
}

func TestGeminiProbe_RedirectNotFollowed_HeaderNotForwarded(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	p := &GeminiProber{apiKey: fakeProbeKey, client: &http.Client{}, baseURL: redirector.URL}
	_, err := p.Probe(context.Background())
	if hits.Load() != 0 {
		t.Fatal("redirect was followed; x-goog-api-key forwarded to another host")
	}
	assertProbeErrClean(t, err)
}
