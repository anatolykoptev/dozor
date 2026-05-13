package main

import (
	"os"
	"testing"
)

// TestResolveBindHost covers the DOZOR_BIND_HOST env-var resolution:
//   - unset → default loopback
//   - empty string → default loopback
//   - "0.0.0.0" → passed through as-is
//   - "127.0.0.1" → passed through as-is
//   - whitespace-padded → trimmed
func TestResolveBindHost(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("DOZOR_BIND_HOST") })

	tests := []struct {
		name    string
		envVal  string
		setEnv  bool
		want    string
	}{
		{
			name:   "unset returns loopback default",
			setEnv: false,
			want:   "127.0.0.1",
		},
		{
			name:   "empty string returns loopback default",
			setEnv: true,
			envVal: "",
			want:   "127.0.0.1",
		},
		{
			name:   "explicit loopback passes through",
			setEnv: true,
			envVal: "127.0.0.1",
			want:   "127.0.0.1",
		},
		{
			name:   "explicit all-interfaces passes through",
			setEnv: true,
			envVal: "0.0.0.0",
			want:   "0.0.0.0",
		},
		{
			name:   "whitespace is trimmed",
			setEnv: true,
			envVal: "  0.0.0.0  ",
			want:   "0.0.0.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			os.Unsetenv("DOZOR_BIND_HOST")
			if tc.setEnv {
				os.Setenv("DOZOR_BIND_HOST", tc.envVal)
			}
			got := resolveBindHost()
			if got != tc.want {
				t.Errorf("resolveBindHost() = %q, want %q", got, tc.want)
			}
		})
	}
}
