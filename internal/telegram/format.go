package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

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

// repairHTMLNesting fixes malformed HTML tag nesting from regex-based conversion.
func repairHTMLNesting(html string) string {
	type tagPos struct {
		tag   string
		start int
	}

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

		if len(tag) >= 3 && tag[1] == '/' {
			closeTag := tag[2 : len(tag)-1]

			matchIdx := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].tag == closeTag {
					matchIdx = k
					break
				}
			}

			if matchIdx < 0 {
				i = j + 1
				continue
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
			stack = append(stack[:matchIdx], reopened...)
		} else {
			tagContent := tag[1 : len(tag)-1]
			tagContent = strings.TrimSuffix(tagContent, "/")
			parts := strings.Fields(tagContent)
			if len(parts) > 0 {
				tagName := parts[0]
				result.WriteString(tag)
				switch tagName {
				case "b", "i", "s", "u", "a", "code", "pre", "blockquote":
					stack = append(stack, tagPos{tag: tagName, start: result.Len()})
				}
			} else {
				result.WriteString(tag)
			}
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
		if len(match) >= 3 {
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

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}
		chunk := text[:maxLen]
		splitAt := strings.LastIndex(chunk, "\n")
		if splitAt <= 0 {
			splitAt = maxLen
		}
		chunks = append(chunks, strings.TrimRight(text[:splitAt], "\n"))
		text = strings.TrimLeft(text[splitAt:], "\n")
	}
	return chunks
}
