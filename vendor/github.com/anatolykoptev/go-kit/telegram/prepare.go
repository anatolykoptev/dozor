package telegram

// PrepareForTelegram detects the markup format of text and returns a
// Telegram-ready (out, parseMode) pair. parseMode is always "HTML" —
// Telegram accepts HTML mode for plain-escaped text just as well.
//
// Routing:
//   - HTML input: SanitizeHTML → RepairHTMLNesting → ("HTML")
//   - Markdown input: MarkdownToHTML (converts + escapes + repairs) →
//     SanitizeHTML (defensive) → ("HTML")
//   - Plain input: EscapeHTML → ("HTML")
//   - Empty input: ("", "HTML")
func PrepareForTelegram(text string) (out string, parseMode string) {
	const mode = "HTML"
	if text == "" {
		return "", mode
	}
	switch Detect(text) {
	case FormatHTML:
		out = RepairHTMLNesting(SanitizeHTML(text)) // belt-and-braces: SanitizeHTML already walks the AST; RepairHTMLNesting catches any edge-case nesting the regex-free pass missed
	case FormatMarkdown:
		out = SanitizeHTML(MarkdownToHTML(text))
	default: // FormatPlain
		out = EscapeHTML(text)
	}
	return out, mode
}
