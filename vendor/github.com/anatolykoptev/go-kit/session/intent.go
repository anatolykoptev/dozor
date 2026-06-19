package session

import (
	"strings"
	"unicode/utf8"
)

// Force keywords that always trigger memory context.
var forceKeywords = []string{
	"помнишь", "помни", "запомни", "вспомни", "мы обсуждали", "мы говорили",
	"remember", "recall", "we discussed", "you mentioned",
	"в прошлый раз", "раньше", "память", "memory",
}

// Skip prefixes for greetings and short replies.
var skipPrefixes = []string{
	"привет", "здравствуй", "добрый", "доброе", "hi", "hello", "hey",
	"спасибо", "thanks", "thank you", "ok", "ок", "да", "нет", "yes", "no",
	"пока", "bye", "good", "до свидания", "до встречи",
}

const (
	minRuneCount     = 5
	skipMaxRuneCount = 25
)

// NeedsMemoryContext returns true if the text is substantive enough
// to warrant loading conversation memory from MemDB.
func NeedsMemoryContext(text string) bool {
	if utf8.RuneCountInString(text) < minRuneCount {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(text))

	for _, kw := range forceKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	// Strip trailing punctuation for matching.
	cleaned := strings.TrimRight(lower, "!?.,;:")

	runeCount := utf8.RuneCountInString(lower)
	if runeCount < skipMaxRuneCount {
		for _, prefix := range skipPrefixes {
			if cleaned == prefix || strings.HasPrefix(cleaned, prefix+" ") ||
				strings.HasPrefix(lower, prefix+" ") || strings.HasPrefix(lower, prefix+",") {
				return false
			}
		}
	}

	return true
}
