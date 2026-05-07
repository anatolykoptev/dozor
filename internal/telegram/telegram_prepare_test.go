package telegram

import (
	"strings"
	"testing"

	tgfmt "github.com/anatolykoptev/go-kit/telegram"
)

// prepareText is the helper under test: it wraps the format-and-escape
// pipeline that sendReply applies to outbound text before calling sendChunked.
// After the swap it will call tgfmt.PrepareForTelegram and discard the mode
// return (always "HTML", matching the tgbotapi.ModeHTML constant used at the
// send site).
func prepareText(text string) string {
	out, _ := tgfmt.PrepareForTelegram(text)
	return out
}

// TestPrepareTextHTMLInput verifies that pre-formatted HTML (as produced by
// buildAutoRemediateMessage) passes through unchanged rather than being
// double-escaped.
//
// Regression for bug 2026-05-06: sendReply always called markdownToTelegramHTML
// which invoked EscapeHTML on the input, turning "<b>" into "&lt;b&gt;".
func TestPrepareTextHTMLInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		// wantContains: all substrings must appear in the output
		wantContains []string
		// wantAbsent: none of these substrings may appear in the output
		wantAbsent []string
	}{
		{
			name:  "pre-formatted HTML from buildAutoRemediateMessage",
			input: "<b>Auto-fix applied</b>\n\n<b>Disk freed:</b>\n  • caches=2920.0 MB",
			wantContains: []string{
				"<b>Auto-fix applied</b>",
				"<b>Disk freed:</b>",
				"caches=2920.0 MB",
			},
			wantAbsent: []string{
				"&lt;b&gt;",
				"&lt;/b&gt;",
			},
		},
		{
			name:  "markdown bold and italic",
			input: "**bold** _italic_",
			wantContains: []string{
				"<b>bold</b>",
				"<i>italic</i>",
			},
			wantAbsent: []string{
				"**bold**",
			},
		},
		{
			name:  "plain text passes through",
			input: "hello world",
			wantContains: []string{
				"hello world",
			},
		},
		{
			name:  "plain text with angle bracket is escaped",
			input: "2 < 3",
			wantContains: []string{
				"2 &lt; 3",
			},
			wantAbsent: []string{
				"2 < 3",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := prepareText(tc.input)
			for _, want := range tc.wantContains {
				if !strings.Contains(out, want) {
					t.Errorf("prepareText(%q) = %q\n  want to contain: %q", tc.input, out, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(out, absent) {
					t.Errorf("prepareText(%q) = %q\n  must NOT contain: %q", tc.input, out, absent)
				}
			}
		})
	}
}

// TestPrepareTextMarkdownToTelegramHTMLRegression verifies that the OLD
// markdownToTelegramHTML behaviour (EscapeHTML on HTML input) would have
// failed on this input — this documents the bug.
func TestPrepareTextOldBehaviourWouldFail(t *testing.T) {
	// markdownToTelegramHTML is the old alias; it still exists in format.go
	// until we remove it. We verify it escapes HTML tags so the regression
	// is clearly documented.
	htmlInput := "<b>Auto-fix applied</b>"
	out := markdownToTelegramHTML(htmlInput)
	if strings.Contains(out, "<b>") {
		t.Skipf("markdownToTelegramHTML already preserves HTML tags (alias may have been updated): %q", out)
	}
	// This is the BUG: if we still get here, the old function escaped the tags.
	if !strings.Contains(out, "&lt;b&gt;") {
		t.Errorf("expected old behaviour to produce &lt;b&gt;, got: %q", out)
	}
}
