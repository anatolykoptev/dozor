package telegram

import tgfmt "github.com/anatolykoptev/go-kit/telegram"

// Aliases for go-kit/telegram functions — keeps internal call sites unchanged.
var (
	stripMarkdown = tgfmt.StripMarkdown
	sanitizeUTF8  = tgfmt.SanitizeUTF8
	splitMessage  = tgfmt.SplitMessage
)

// markdownToTelegramHTML prepares text for Telegram HTML mode by auto-detecting
// the input format (HTML/Markdown/Plain) and routing through the appropriate
// sanitizer. Replaces the former tgfmt.MarkdownToHTML alias.
//
// PrepareForTelegram always returns "HTML" mode; we keep the existing
// tgbotapi.ModeHTML constant at the send site (sendChunked call in telegram.go).
func markdownToTelegramHTML(text string) string {
	out, _ := tgfmt.PrepareForTelegram(text)
	return out
}

