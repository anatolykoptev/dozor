package telegram

import tgfmt "github.com/anatolykoptev/go-kit/telegram"

// Aliases for go-kit/telegram functions — keeps internal call sites unchanged.
var (
	markdownToTelegramHTML = tgfmt.MarkdownToHTML
	stripMarkdown          = tgfmt.StripMarkdown
	sanitizeUTF8           = tgfmt.SanitizeUTF8
	splitMessage           = tgfmt.SplitMessage
)

// CompactForTelegram re-exports the go-kit function for use by telegram.go.
var CompactForTelegram = tgfmt.CompactForTelegram
