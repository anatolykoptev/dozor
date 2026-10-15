package engine

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Issue #223: maskKey used to keep the first 11 characters of the key (and the
// whole key when it was 11 characters or shorter). Every Google key starts
// "AIzaSy", so the label leaked 5 random characters into the alert Service ->
// Telegram and the triage prompt. The label must now be a hash fingerprint.
func TestMaskKey_RevealsNoKeyMaterial(t *testing.T) {
	t.Parallel()
	keys := []string{
		fakeGeminiKey,
		"AIzaSyTestKey12345",
		"short",       // <= the old 11-char cut-off: used to be returned verbatim
		"AIzaSy12345", // exactly 11
	}
	for _, key := range keys {
		masked := maskKey(key)
		// No 4-char window of the key may appear in the label (a hex
		// fingerprint cannot contain "AIza"/"zaSy"/"hort"...).
		for i := 0; i+4 <= len(key); i++ {
			if strings.Contains(masked, key[i:i+4]) {
				t.Errorf("maskKey(%q) = %q leaks key fragment %q", key, masked, key[i:i+4])
			}
		}
	}
}

func TestMaskKey_StableAndDistinct(t *testing.T) {
	t.Parallel()
	a1, a2 := maskKey(fakeGeminiKey), maskKey(fakeGeminiKey)
	if a1 != a2 {
		t.Errorf("maskKey not stable for the same key: %q vs %q", a1, a2)
	}
	other := maskKey(fakeGeminiKey + "x")
	if a1 == other {
		t.Errorf("maskKey collides for different keys: %q", a1)
	}
	if a1 == "" {
		t.Error("maskKey returned empty label")
	}
}

// The label ends up in Alert.Service via checkGeminiKey; assert on the real path.
func TestCheckGeminiKey_ServiceLabelHasNoKeyPrefix(t *testing.T) {
	t.Parallel()
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &hostRewriteTransport{base: http.DefaultTransport, target: "http://dozor-host-does-not-exist.invalid"},
	}
	a := checkGeminiKey(t.Context(), client, fakeGeminiKey)
	if a == nil {
		t.Fatal("expected an alert")
	}
	if strings.Contains(a.Service, fakeGeminiKey[:6]) {
		t.Errorf("Service %q leaks the key prefix %q", a.Service, fakeGeminiKey[:6])
	}
	if !strings.Contains(a.Service, maskKey(fakeGeminiKey)) {
		t.Errorf("Service %q does not carry the fingerprint label %q", a.Service, maskKey(fakeGeminiKey))
	}
}

func TestStripURLFromErr_NilSafe(t *testing.T) {
	t.Parallel()
	const secretURL = "https://example.invalid/v1?key=" + fakeGeminiKey
	cases := map[string]error{
		"nil error":           nil,
		"url.Error nil cause": &url.Error{Op: "Get", URL: secretURL},
		"wrapped, nil cause":  errors.Join(errors.New("ctx"), &url.Error{Op: "Post", URL: secretURL}),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			got := stripURLFromErr(err) // must not panic
			if strings.Contains(got, fakeGeminiKey) || strings.Contains(got, "https://") {
				t.Errorf("stripURLFromErr leaked URL/key: %q", got)
			}
		})
	}
}

// The CLIProxy "unreachable" alert used raw err.Error(), which embeds the
// request URL (and any credential in it).
func TestCheckProxyModel_Unreachable_DescriptionHasNoURL(t *testing.T) {
	t.Parallel()
	a := checkProxyModel(t.Context(), "http://dozor-proxy-nohost.invalid/v1?token=SECRETQUERY", "k", "m")
	if a == nil {
		t.Fatal("expected an alert")
	}
	if a.Title != "unreachable" {
		t.Fatalf("title = %q, want unreachable", a.Title)
	}
	if strings.Contains(a.Description, "SECRETQUERY") || strings.Contains(a.Description, "http") {
		t.Errorf("Description leaks the request URL: %q", a.Description)
	}
}
