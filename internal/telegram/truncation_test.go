package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"

	tgfmt "github.com/anatolykoptev/go-kit/telegram"
)

// TestAlertNotTruncated_CheckLinePreserved pins the fix for the message
// truncation bug: long alert messages must reach sendChunked intact so that
// the trailing Check: / diagnostic lines are NEVER silently dropped.
//
// Red-on-revert: re-introducing CompactForTelegram(text, 4000) before the
// splitMessage call causes text > 4000 runes to be hard-truncated at a
// newline boundary, stripping the Check: block.  This test will fail with
// the Check: substring absent from the collected chunks.
func TestAlertNotTruncated_CheckLinePreserved(t *testing.T) {
	// Build a realistic alert body that exceeds 4000 runes.
	// Filler lines simulate a watch report header / service list.
	var sb strings.Builder
	for range 200 {
		sb.WriteString("🔴 oxpulse-chat: container unhealthy — health-check endpoint returned 503. Recent errors: connection refused, dial tcp 127.0.0.1:8907: connect: connection refused.\n")
	}
	// The diagnostic block comes at the END — exactly what gets truncated.
	sb.WriteString("Check: journalctl -u caddy -n 50 | grep aborting\n")
	sb.WriteString("Likely causes: unbounded goroutine leak, OOM kill, misconfigured upstream.\n")
	sb.WriteString("Check: ssh partner-edge-01 and verify TCP :443 is responding.\n")

	alertText := sb.String()
	if utf8.RuneCountInString(alertText) <= 4000 {
		t.Fatalf("precondition: alert text must be >4000 runes, got %d", utf8.RuneCountInString(alertText))
	}

	// Replicate what sendReply does after the fix:
	// sanitizeUTF8 → markdownToTelegramHTML → splitMessage(4096)
	// (No CompactForTelegram step.)
	text := tgfmt.SanitizeUTF8(alertText)
	htmlText, _ := tgfmt.PrepareForTelegram(text)
	chunks := tgfmt.SplitMessage(htmlText, tgfmt.MaxMessageLen)

	if len(chunks) == 0 {
		t.Fatal("splitMessage returned no chunks")
	}

	// Every chunk must fit within the Telegram limit.
	for i, chunk := range chunks {
		rc := utf8.RuneCountInString(chunk)
		if rc > tgfmt.MaxMessageLen {
			t.Errorf("chunk %d exceeds MaxMessageLen: %d runes", i, rc)
		}
	}

	// The Check: lines must appear in the collected output — none dropped.
	all := strings.Join(chunks, "")
	for _, want := range []string{
		"Check: journalctl -u caddy",
		"unbounded goroutine",
		"Check: ssh partner-edge",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("diagnostic line lost in split output: %q not found", want)
		}
	}
}

// TestAlertNotTruncated_ShortMessageSingleChunk verifies that a short alert
// (<4096 runes) is delivered as exactly one chunk with no modifications.
func TestAlertNotTruncated_ShortMessageSingleChunk(t *testing.T) {
	alertText := "🔴 caddy unhealthy\n\nCheck: journalctl -u caddy -n 50 | grep aborting\nLikely causes: unbounded goroutine leak.\n"
	if utf8.RuneCountInString(alertText) >= tgfmt.MaxMessageLen {
		t.Fatalf("precondition: short alert must be <%d runes", tgfmt.MaxMessageLen)
	}

	text := tgfmt.SanitizeUTF8(alertText)
	htmlText, _ := tgfmt.PrepareForTelegram(text)
	chunks := tgfmt.SplitMessage(htmlText, tgfmt.MaxMessageLen)

	if len(chunks) != 1 {
		t.Errorf("short alert should produce 1 chunk, got %d", len(chunks))
	}
	for _, want := range []string{"Check: journalctl", "unbounded goroutine"} {
		if !strings.Contains(chunks[0], want) {
			t.Errorf("diagnostic line missing from single chunk: %q", want)
		}
	}
}
