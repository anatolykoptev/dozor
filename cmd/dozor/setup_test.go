package main

import "testing"

// TestResolveBindHost covers the DOZOR_BIND_HOST env-var resolution:
//   - unset / empty / whitespace-only → default loopback
//   - "0.0.0.0" → passed through as-is
//   - "127.0.0.1" → passed through as-is
//   - surrounding whitespace → trimmed
//
// Uses t.Setenv for per-subtest cleanup and race-safety. resolveBindHost
// treats unset and empty identically (both yield the default), so we pass
// "" to model the unset case rather than introduce a separate primitive.
func TestResolveBindHost(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   string
	}{
		{
			name:   "empty (models unset) returns loopback default",
			envVal: "",
			want:   "127.0.0.1",
		},
		{
			name:   "whitespace-only returns loopback default",
			envVal: "   ",
			want:   "127.0.0.1",
		},
		{
			name:   "explicit loopback passes through",
			envVal: "127.0.0.1",
			want:   "127.0.0.1",
		},
		{
			name:   "explicit all-interfaces passes through",
			envVal: "0.0.0.0",
			want:   "0.0.0.0",
		},
		{
			name:   "whitespace around value is trimmed",
			envVal: "  0.0.0.0  ",
			want:   "0.0.0.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOZOR_BIND_HOST", tc.envVal)
			got := resolveBindHost()
			if got != tc.want {
				t.Errorf("resolveBindHost(env=%q) = %q, want %q", tc.envVal, got, tc.want)
			}
		})
	}
}

func TestIsLoopbackBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true,
		"::1":       true,
		"localhost": true,
		"0.0.0.0":   false,
		"10.0.0.1":  false,
		"":          false,
	}
	for in, want := range cases {
		if got := isLoopbackBind(in); got != want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", in, got, want)
		}
	}
}
