package engine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes_CyrillicRuneSafe(t *testing.T) {
	// 10 Cyrillic runes = 20 bytes. A byte-slice at an odd offset would split a
	// rune; TruncateRunes must cut on a rune boundary and stay valid UTF-8.
	s := strings.Repeat("я", 10) // 'я' is 2 bytes
	got := TruncateRunes(s, 5)
	if utf8.RuneCountInString(got) != 5 {
		t.Fatalf("want 5 runes, got %d", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("result is not valid UTF-8: %q", got)
	}
}

func TestTruncateRunes_Bounds(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"}, // shorter than max → unchanged
		{"hello", 5, "hello"},  // exactly max → unchanged
		{"hello", 3, "hel"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := TruncateRunes(c.s, c.max); got != c.want {
			t.Errorf("TruncateRunes(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

func TestTruncateRunesEllipsis(t *testing.T) {
	if got := TruncateRunesEllipsis("hello", 10); got != "hello" {
		t.Errorf("no truncation expected, got %q", got)
	}
	// Truncated: result must be exactly max runes with a trailing "...".
	got := TruncateRunesEllipsis("hello world", 8)
	if utf8.RuneCountInString(got) != 8 {
		t.Fatalf("want 8 runes, got %d (%q)", utf8.RuneCountInString(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("want trailing ellipsis, got %q", got)
	}
	if got != "hello..." {
		t.Errorf("want %q, got %q", "hello...", got)
	}
}

func TestTruncateRunesEllipsis_CyrillicAtCapBoundary(t *testing.T) {
	// MaxCaptionRunes Cyrillic runes + overflow → result is exactly the cap,
	// valid UTF-8, ends with the ellipsis (the real caption-truncation case).
	s := strings.Repeat("ё", MaxCaptionRunes+50)
	got := TruncateRunesEllipsis(s, MaxCaptionRunes)
	if utf8.RuneCountInString(got) != MaxCaptionRunes {
		t.Fatalf("want %d runes, got %d", MaxCaptionRunes, utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("caption truncation produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("want trailing ellipsis")
	}
}

func TestTruncateRunesEllipsis_TinyMax(t *testing.T) {
	// max <= 3 → no room for ellipsis, plain rune-safe cut.
	got := TruncateRunesEllipsis("hello", 2)
	if got != "he" {
		t.Errorf("want %q, got %q", "he", got)
	}
}
