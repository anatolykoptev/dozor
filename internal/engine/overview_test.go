package engine

import "testing"

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
		{1073741824, "1.0 GB"},
		{2147483648, "2.0 GB"},
		{1610612736, "1.5 GB"},
	}
	for _, c := range cases {
		got := formatBytes(c.input)
		if got != c.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", c.input, got, c.expected)
		}
	}
}
