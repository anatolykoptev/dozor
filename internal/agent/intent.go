package agent

import (
	"strings"
	"unicode/utf8"
)

const (
	minSubstantiveRunes = 5
	maxTrivialRunes     = 25
)

var trivialPrefixes = []string{
	"привет", "здравствуй", "добрый", "hi", "hello", "hey",
	"спасибо", "thanks", "ok", "ок", "да", "нет", "yes", "no",
	"пока", "bye", "good",
}

// NeedsMemoryContext returns true if the message is substantive enough
// to warrant a knowledge base lookup. Filters out greetings and trivial inputs.
func NeedsMemoryContext(text string) bool {
	if utf8.RuneCountInString(text) < minSubstantiveRunes {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range trivialPrefixes {
		if lower == prefix || (strings.HasPrefix(lower, prefix+" ") && utf8.RuneCountInString(lower) < maxTrivialRunes) {
			return false
		}
	}
	return true
}
