package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func keyFor(peer string, xff ...string) string {
	r := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	r.RemoteAddr = peer
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return sourceKey(r, true)
}

func TestSourceKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		peer string
		xff  []string
		want string
	}{
		{"loopback peer, single XFF", "127.0.0.1:1", []string{"198.51.100.7"}, "198.51.100.7"},
		{"loopback peer, rightmost of many (leftmost is client-supplied)", "127.0.0.1:1", []string{"6.6.6.6, 198.51.100.7, 203.0.113.9"}, "203.0.113.9"},
		{"rightmost across repeated header lines", "127.0.0.1:1", []string{"6.6.6.6", "203.0.113.9"}, "203.0.113.9"},
		{"IPv6 loopback peer", "[::1]:1", []string{"2001:db8::7"}, "2001:db8::7"},
		{"IPv4-mapped loopback peer", "[::ffff:127.0.0.1]:1", []string{"198.51.100.7"}, "198.51.100.7"},
		{"invalid XFF falls back to peer", "127.0.0.1:1", []string{"not-an-ip"}, "127.0.0.1"},
		{"invalid rightmost falls back to peer", "127.0.0.1:1", []string{"198.51.100.7, evil"}, "127.0.0.1"},
		{"empty XFF falls back to peer", "127.0.0.1:1", []string{" "}, "127.0.0.1"},
		{"non-loopback peer XFF ignored", "172.17.0.5:1", []string{"198.51.100.7"}, "172.17.0.5"},
		{"public peer XFF ignored", "8.8.8.8:1", []string{"198.51.100.7"}, "8.8.8.8"},
		{"no XFF", "10.0.0.9:1", nil, "10.0.0.9"},
		{"unparsable peer", "garbage", nil, "unknown"},
	}
	for _, tc := range cases {
		if got := keyFor(tc.peer, tc.xff...); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSourceKey_TrustDisabledIgnoresXFF(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := sourceKey(r, false); got != "127.0.0.1" {
		t.Errorf("key = %q, want peer 127.0.0.1", got)
	}
}

func TestParseTrustProxy(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"": true, "1": true, "true": true, "0": false, "false": false, "FALSE": false, " 0 ": false} {
		if got := parseTrustProxy(in); got != want {
			t.Errorf("parseTrustProxy(%q) = %v, want %v", in, got, want)
		}
	}
}
