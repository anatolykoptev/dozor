package engine

// MaxCaptionRunes is Telegram's hard cap on a photo caption, in characters
// (runes), not bytes. Captions longer than this are rejected by the Bot API.
const MaxCaptionRunes = 1024

// TruncateRunes returns s limited to at most maxRunes runes, never splitting a
// multi-byte UTF-8 codepoint (a plain byte-slice like s[:n] would corrupt the
// last rune when n falls mid-codepoint — e.g. Cyrillic, 2 bytes/rune).
// maxRunes <= 0 returns "".
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// TruncateRunesEllipsis caps s at maxRunes runes; when truncation occurs the
// tail is replaced by "..." so the result stays within maxRunes. Rune-safe
// (see TruncateRunes). For maxRunes <= 3 the ellipsis is omitted.
func TruncateRunesEllipsis(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(r[:maxRunes])
	}
	return string(r[:maxRunes-3]) + "..."
}
