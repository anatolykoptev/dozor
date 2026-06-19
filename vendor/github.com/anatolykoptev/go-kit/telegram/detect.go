package telegram

import "regexp"

// Format represents the detected markup format of a text string.
type Format int

const (
	// FormatPlain indicates plain text with no markup.
	FormatPlain Format = iota
	// FormatHTML indicates HTML markup (Telegram-compatible or raw HTML).
	FormatHTML
	// FormatMarkdown indicates Markdown markup.
	FormatMarkdown
)

// String returns a human-readable name for the format.
func (f Format) String() string {
	switch f {
	case FormatHTML:
		return "HTML"
	case FormatMarkdown:
		return "Markdown"
	default:
		return "Plain"
	}
}

// reHTMLTag matches any Telegram-relevant HTML tag.
// Presence of any such tag indicates the text is HTML.
var reHTMLTag = regexp.MustCompile(
	`(?i)<(b|strong|i|em|u|ins|s|strike|del|a|code|pre|blockquote|span|p|div|br|h[1-6]|ul|ol|li|tg-spoiler)\b`,
)

// reMDHeuristics holds individual markdown signals, each sufficient for detection.
var reMDHeuristics = []*regexp.Regexp{
	regexp.MustCompile(`\*\*\w`),                // **word
	regexp.MustCompile(`__\w`),                  // __word
	regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`),   // [text](url)
	regexp.MustCompile("(?m)^```"),              // fenced code block
	regexp.MustCompile(`(?m)^# `),              // ATX heading
	regexp.MustCompile(`(?m)^- `),              // list item
}

// Detect heuristically identifies the markup format of text.
// Returns FormatHTML if any Telegram-relevant HTML tag is found.
// Returns FormatMarkdown if Markdown patterns are found.
// Returns FormatPlain otherwise.
func Detect(text string) Format {
	if reHTMLTag.MatchString(text) {
		return FormatHTML
	}
	for _, re := range reMDHeuristics {
		if re.MatchString(text) {
			return FormatMarkdown
		}
	}
	return FormatPlain
}
