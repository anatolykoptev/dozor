package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// codeBlockMatchGroups is the expected number of regex match groups for code block extraction.
	codeBlockMatchGroups = 3
	// htmlTagMinLen is the minimum length of an HTML tag (e.g. "<b>").
	htmlTagMinLen = 3
)

// --- Compact LLM output for Telegram ---

// Precompiled regexes for CompactForTelegram.
var (
	reVerboseSection = regexp.MustCompile(`(?mi)^#+\s+(recent errors|error log|raw logs?|detailed analysis|full output|stack trace|verbose).*$`)
	reLargeCodeBlock = regexp.MustCompile("(?s)```[\\w]*\n.{500,}?```")
)

// CompactForTelegram truncates verbose LLM responses for Telegram delivery.
// Runs on raw markdown BEFORE HTML conversion. Pass-through if text ≤ maxChars.
func CompactForTelegram(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}

	// Strip large code blocks (>500 chars) — replace with one-liner.
	text = reLargeCodeBlock.ReplaceAllString(text, "```\n(code block trimmed)\n```")

	// If still fits, done.
	if len(text) <= maxChars {
		return text
	}

	// Find first verbose section heading and truncate there.
	if loc := reVerboseSection.FindStringIndex(text); loc != nil && loc[0] > 200 {
		text = strings.TrimRight(text[:loc[0]], "\n ") + "\n\n… _(truncated)_"
		if len(text) <= maxChars {
			return text
		}
	}

	// Hard truncate at maxChars on a newline boundary.
	cut := text[:maxChars-30]
	if nl := strings.LastIndex(cut, "\n"); nl > len(cut)/2 {
		cut = cut[:nl]
	}
	return strings.TrimRight(cut, "\n ") + "\n\n… _(truncated)_"
}

// --- Markdown → Telegram HTML conversion ---
// Adapted from Vaelor's pkg/channels/telegram.go

// Precompiled regexes for markdown→HTML conversion.
var (
	reHeading     = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reBlockquote  = regexp.MustCompile(`(?m)(^&gt;[ \t]?.*$\n?)+`)
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldStar    = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder   = regexp.MustCompile(`__(.+?)__`)
	reItalicStar  = regexp.MustCompile(`\*([^*\n]+)\*`)
	reItalicUnder = regexp.MustCompile(`_([^_\n]+)_`)
	reStrike      = regexp.MustCompile(`~~(.+?)~~`)
	reListItem    = regexp.MustCompile(`(?m)^[-*]\s+`)
	reHRule       = regexp.MustCompile(`(?m)^[-*_]{3,}\s*$`)
)

// Precompiled regexes for stripMarkdown (plain-text fallback).
var (
	reStripCodeFence  = regexp.MustCompile("(?m)^```\\w*\n?")
	reStripBoldItalic = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reStripBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reStripBoldU      = regexp.MustCompile(`__(.+?)__`)
	reStripItalicS    = regexp.MustCompile(`\*(.+?)\*`)
	reStripItalicU    = regexp.MustCompile(`_(.+?)_`)
	reStripStrike     = regexp.MustCompile("~~(.+?)~~")
	reStripInline     = regexp.MustCompile("`(.+?)`")
	reStripHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reStripListItem   = regexp.MustCompile(`(?m)^[-*+]\s`)
	reStripBlockquote = regexp.MustCompile(`(?m)^>\s?`)
	reStripLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

func sanitizeUTF8(text string) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.ReplaceAll(text, "\x00", "")
	return text
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	// 1. Extract code blocks and inline codes (protect from all transformations).
	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	// 2. Escape HTML entities.
	text = escapeHTML(text)

	// 3. Headings → bold.
	text = reHeading.ReplaceAllString(text, "<b>$1</b>")

	// 4. Blockquotes → <blockquote>.
	text = convertBlockquotes(text)

	// 5. Horizontal rules → thin line.
	text = reHRule.ReplaceAllString(text, "———")

	// 6. Links.
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// 7. Bold + italic combos (order matters: *** before ** before *).
	text = reBoldStar.ReplaceAllString(text, "<b><i>$1</i></b>")
	text = reBold.ReplaceAllString(text, "<b>$1</b>")
	text = reBoldUnder.ReplaceAllString(text, "<b>$1</b>")

	// 8. Strikethrough.
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	// 9. List items — before single-* italic.
	text = reListItem.ReplaceAllString(text, "• ")

	// 10. Italic.
	text = reItalicStar.ReplaceAllString(text, "<i>$1</i>")
	text = reItalicUnder.ReplaceAllString(text, "<i>$1</i>")

	// 11. Restore inline codes.
	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), "<code>"+escaped+"</code>")
	}

	// 12. Restore code blocks with language tags.
	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		lang := codeBlocks.langs[i]
		if lang != "" {
			text = strings.ReplaceAll(text, fmt.Sprintf("\x00CB%d\x00", i),
				"<pre><code class=\"language-"+lang+"\">"+escaped+"</code></pre>")
		} else {
			text = strings.ReplaceAll(text, fmt.Sprintf("\x00CB%d\x00", i),
				"<pre><code>"+escaped+"</code></pre>")
		}
	}

	// 13. Repair any mismatched HTML nesting.
	text = repairHTMLNesting(text)

	return text
}

func stripMarkdown(text string) string {
	text = reStripCodeFence.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "```", "")
	text = reStripBoldItalic.ReplaceAllString(text, "$1")
	text = reStripBold.ReplaceAllString(text, "$1")
	text = reStripBoldU.ReplaceAllString(text, "$1")
	text = reStripItalicS.ReplaceAllString(text, "$1")
	text = reStripItalicU.ReplaceAllString(text, "$1")
	text = reStripStrike.ReplaceAllString(text, "$1")
	text = reStripInline.ReplaceAllString(text, "$1")
	text = reStripHeading.ReplaceAllString(text, "")
	text = reStripListItem.ReplaceAllString(text, "- ")
	text = reStripBlockquote.ReplaceAllString(text, "")
	text = reStripLink.ReplaceAllString(text, "$1 ($2)")
	return text
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

func convertBlockquotes(text string) string {
	return reBlockquote.ReplaceAllStringFunc(text, func(block string) string {
		lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
		var cleaned []string
		for _, line := range lines {
			line = strings.TrimPrefix(line, "&gt; ")
			line = strings.TrimPrefix(line, "&gt;")
			cleaned = append(cleaned, line)
		}
		return "<blockquote>" + strings.Join(cleaned, "\n") + "</blockquote>\n"
	})
}

// tagPos records an open HTML tag and its position in the output buffer.
type tagPos struct {
	tag   string
	start int
}

// handleCloseTag processes a closing HTML tag: finds the matching opener in the
// stack, emits closing tags for any intervening openers, emits the matched close
// tag, then reopens the intervening tags and returns the updated stack.
func handleCloseTag(tag string, stack []tagPos, result *strings.Builder) []tagPos {
	closeTag := tag[2 : len(tag)-1]

	matchIdx := -1
	for k := len(stack) - 1; k >= 0; k-- {
		if stack[k].tag == closeTag {
			matchIdx = k
			break
		}
	}

	if matchIdx < 0 {
		return stack // unmatched closer — discard
	}

	for k := len(stack) - 1; k > matchIdx; k-- {
		result.WriteString("</" + stack[k].tag + ">")
	}
	result.WriteString(tag)

	reopened := make([]tagPos, 0, len(stack)-matchIdx-1)
	for k := matchIdx + 1; k < len(stack); k++ {
		reopenTag := "<" + stack[k].tag + ">"
		result.WriteString(reopenTag)
		reopened = append(reopened, tagPos{tag: stack[k].tag, start: result.Len()})
	}
	return append(stack[:matchIdx], reopened...)
}

// handleOpenTag writes the opening tag to result and, for tracked Telegram tags,
// pushes an entry onto the stack. Returns the updated stack.
func handleOpenTag(tag string, stack []tagPos, result *strings.Builder) []tagPos {
	tagContent := tag[1 : len(tag)-1]
	tagContent = strings.TrimSuffix(tagContent, "/")
	parts := strings.Fields(tagContent)
	result.WriteString(tag)
	if len(parts) > 0 {
		switch parts[0] {
		case "b", "i", "s", "u", "a", "code", "pre", "blockquote":
			stack = append(stack, tagPos{tag: parts[0], start: result.Len()})
		}
	}
	return stack
}

// repairHTMLNesting fixes malformed HTML tag nesting from regex-based conversion.
func repairHTMLNesting(html string) string {
	var result strings.Builder
	result.Grow(len(html))
	var stack []tagPos
	i := 0

	for i < len(html) {
		if html[i] != '<' {
			result.WriteByte(html[i])
			i++
			continue
		}

		j := strings.IndexByte(html[i:], '>')
		if j < 0 {
			result.WriteString(html[i:])
			break
		}
		j += i
		tag := html[i : j+1]

		if len(tag) >= htmlTagMinLen && tag[1] == '/' {
			stack = handleCloseTag(tag, stack, &result)
		} else {
			stack = handleOpenTag(tag, stack, &result)
		}
		i = j + 1
	}

	for k := len(stack) - 1; k >= 0; k-- {
		result.WriteString("</" + stack[k].tag + ">")
	}

	return result.String()
}

type codeBlockMatch struct {
	text  string
	codes []string
	langs []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	re := regexp.MustCompile("```([\\w]*)\\n?([\\s\\S]*?)```")

	var codes []string
	var langs []string
	idx := 0
	text = re.ReplaceAllStringFunc(text, func(m string) string {
		match := re.FindStringSubmatch(m)
		lang := ""
		code := m
		if len(match) >= codeBlockMatchGroups {
			lang = match[1]
			code = match[2]
		}
		langs = append(langs, lang)
		codes = append(codes, code)
		placeholder := fmt.Sprintf("\x00CB%d\x00", idx)
		idx++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes, langs: langs}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	re := regexp.MustCompile("`([^`]+)`")

	var codes []string
	idx := 0
	text = re.ReplaceAllStringFunc(text, func(m string) string {
		match := re.FindStringSubmatch(m)
		code := m
		if len(match) >= 2 {
			code = match[1]
		}
		codes = append(codes, code)
		placeholder := fmt.Sprintf("\x00IC%d\x00", idx)
		idx++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

// splitMessage splits text into chunks that fit Telegram's message limit,
// preserving valid HTML across chunk boundaries by closing and reopening tags.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	// First pass: split on newline boundaries (raw, ignoring tags).
	var rawChunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			rawChunks = append(rawChunks, text)
			break
		}
		chunk := text[:maxLen]
		splitAt := strings.LastIndex(chunk, "\n")
		if splitAt <= 0 {
			splitAt = maxLen
		}
		rawChunks = append(rawChunks, strings.TrimRight(text[:splitAt], "\n"))
		text = strings.TrimLeft(text[splitAt:], "\n")
	}

	// Second pass: fix HTML tags across chunk boundaries.
	var openTags []string // tags open at end of previous chunk
	result := make([]string, 0, len(rawChunks))

	for _, chunk := range rawChunks {
		// Prepend reopening tags from previous chunk.
		if len(openTags) > 0 {
			chunk = strings.Join(openTags, "") + chunk
		}

		// Track which tags are open at end of this chunk.
		openTags = unclosedTags(chunk)

		// Append closing tags in reverse order.
		if len(openTags) > 0 {
			var closers strings.Builder
			for i := len(openTags) - 1; i >= 0; i-- {
				tagName := openTags[i][1 : len(openTags[i])-1]
				// Strip attributes (e.g. <a href="..."> → a)
				if sp := strings.IndexByte(tagName, ' '); sp > 0 {
					tagName = tagName[:sp]
				}
				closers.WriteString("</" + tagName + ">")
			}
			chunk += closers.String()
		}

		result = append(result, chunk)
	}
	return result
}

// parseTagName extracts the tag name from an opening tag string.
// E.g. `<a href="...">` → `a`, `<b>` → `b`.
func parseTagName(openTag string) string {
	inner := openTag[1 : len(openTag)-1]
	if sp := strings.IndexByte(inner, ' '); sp > 0 {
		return inner[:sp]
	}
	return inner
}

// popMatchingTag removes the rightmost opener whose tag name matches closeTag
// from stack, returning the updated slice.
func popMatchingTag(stack []string, closeTag string) []string {
	for k := len(stack) - 1; k >= 0; k-- {
		if parseTagName(stack[k]) == closeTag {
			return append(stack[:k], stack[k+1:]...)
		}
	}
	return stack
}

// unclosedTags returns opening tags (e.g. "<b>", "<code>") that remain
// unclosed at the end of the HTML fragment.
func unclosedTags(html string) []string {
	var stack []string
	i := 0
	for i < len(html) {
		lt := strings.IndexByte(html[i:], '<')
		if lt < 0 {
			break
		}
		lt += i
		gt := strings.IndexByte(html[lt:], '>')
		if gt < 0 {
			break
		}
		gt += lt
		tag := html[lt : gt+1]
		i = gt + 1

		if len(tag) < htmlTagMinLen {
			continue
		}

		if tag[1] == '/' {
			stack = popMatchingTag(stack, tag[2:len(tag)-1])
		} else {
			// Opening tag — only track Telegram-supported formatting tags.
			tagContent := strings.TrimSuffix(tag[1:len(tag)-1], "/")
			parts := strings.Fields(tagContent)
			if len(parts) > 0 {
				switch parts[0] {
				case "b", "i", "s", "u", "a", "code", "pre", "blockquote":
					stack = append(stack, tag)
				}
			}
		}
	}
	return stack
}
