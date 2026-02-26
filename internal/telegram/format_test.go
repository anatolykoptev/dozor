package telegram

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseTagName
// ---------------------------------------------------------------------------

func TestParseTagName(t *testing.T) {
	tests := []struct {
		name    string
		openTag string
		want    string
	}{
		{"simple bold", "<b>", "b"},
		{"simple italic", "<i>", "i"},
		{"pre tag", "<pre>", "pre"},
		{"code tag", "<code>", "code"},
		{"anchor with href", `<a href="https://example.com">`, "a"},
		{"code with class", `<code class="language-go">`, "code"},
		{"blockquote tag", "<blockquote>", "blockquote"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTagName(tc.openTag)
			if got != tc.want {
				t.Errorf("parseTagName(%q) = %q, want %q", tc.openTag, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// popMatchingTag
// ---------------------------------------------------------------------------

func TestPopMatchingTag(t *testing.T) {
	tests := []struct {
		name     string
		stack    []string
		closeTag string
		want     []string
	}{
		{
			name:     "pop last matching",
			stack:    []string{"<b>", "<i>"},
			closeTag: "i",
			want:     []string{"<b>"},
		},
		{
			name:     "pop first when interleaved",
			stack:    []string{"<b>", "<i>", "<b>"},
			closeTag: "b",
			want:     []string{"<b>", "<i>"},
		},
		{
			name:     "no match leaves stack unchanged",
			stack:    []string{"<b>", "<i>"},
			closeTag: "code",
			want:     []string{"<b>", "<i>"},
		},
		{
			name:     "pop from single element",
			stack:    []string{"<b>"},
			closeTag: "b",
			want:     []string{},
		},
		{
			name:     "empty stack",
			stack:    []string{},
			closeTag: "b",
			want:     []string{},
		},
		{
			name:     "pop anchor with attributes",
			stack:    []string{`<a href="url">`},
			closeTag: "a",
			want:     []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := popMatchingTag(tc.stack, tc.closeTag)
			if len(got) != len(tc.want) {
				t.Fatalf("popMatchingTag: got %v (len %d), want %v (len %d)", got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("popMatchingTag[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// unclosedTags
// ---------------------------------------------------------------------------

func TestUnclosedTags(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "empty string",
			html: "",
			want: nil,
		},
		{
			name: "no tags",
			html: "plain text",
			want: nil,
		},
		{
			name: "fully closed single tag",
			html: "<b>hello</b>",
			want: nil,
		},
		{
			name: "unclosed bold",
			html: "<b>hello",
			want: []string{"<b>"},
		},
		{
			name: "unclosed italic after closed bold",
			html: "<b>bold</b> <i>italic",
			want: []string{"<i>"},
		},
		{
			name: "interleaved tags — both eventually closed in HTML",
			html: "<b><i>text</i></b>",
			want: nil,
		},
		{
			name: "nested unclosed",
			html: "<b><i>text",
			want: []string{"<b>", "<i>"},
		},
		{
			name: "untracked tag div not tracked",
			html: "<div>text",
			want: nil,
		},
		{
			name: "anchor with href",
			html: `<a href="https://example.com">link`,
			want: []string{`<a href="https://example.com">`},
		},
		{
			name: "code inside pre — both unclosed",
			html: "<pre><code>snippet",
			want: []string{"<pre>", "<code>"},
		},
		{
			name: "self-closing style tag ignored",
			html: "<br/>text",
			want: nil,
		},
		{
			name: "unmatched close discards nothing",
			html: "</b>text",
			want: nil,
		},
		{
			name: "all Telegram formatting tags closed",
			html: "<b>b</b><i>i</i><s>s</s><u>u</u><code>c</code><pre>p</pre><blockquote>q</blockquote>",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := unclosedTags(tc.html)
			if len(got) != len(tc.want) {
				t.Fatalf("unclosedTags(%q) = %v (len %d), want %v (len %d)", tc.html, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("unclosedTags[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleOpenTag
// ---------------------------------------------------------------------------

func TestHandleOpenTag(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		initStack []tagPos
		wantOut   string
		wantDepth int // expected stack depth after call
	}{
		{
			name:      "bold tracked",
			tag:       "<b>",
			initStack: nil,
			wantOut:   "<b>",
			wantDepth: 1,
		},
		{
			name:      "div not tracked",
			tag:       "<div>",
			initStack: nil,
			wantOut:   "<div>",
			wantDepth: 0,
		},
		{
			name:      "anchor with href tracked",
			tag:       `<a href="url">`,
			initStack: nil,
			wantOut:   `<a href="url">`,
			wantDepth: 1,
		},
		{
			name:      "code tag tracked",
			tag:       "<code>",
			initStack: nil,
			wantOut:   "<code>",
			wantDepth: 1,
		},
		{
			name:      "self-closing slash stripped from tag name",
			tag:       "<br/>",
			initStack: nil,
			wantOut:   "<br/>",
			wantDepth: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			stack := handleOpenTag(tc.tag, tc.initStack, &buf)
			if buf.String() != tc.wantOut {
				t.Errorf("handleOpenTag output: got %q, want %q", buf.String(), tc.wantOut)
			}
			if len(stack) != tc.wantDepth {
				t.Errorf("handleOpenTag stack depth: got %d, want %d", len(stack), tc.wantDepth)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleCloseTag
// ---------------------------------------------------------------------------

func TestHandleCloseTag(t *testing.T) {
	t.Run("matched close emits tag", func(t *testing.T) {
		var buf strings.Builder
		stack := []tagPos{{tag: "b", start: 0}}
		buf.WriteString("<b>hello")
		stack = handleCloseTag("</b>", stack, &buf)
		if !strings.HasSuffix(buf.String(), "</b>") {
			t.Errorf("expected </b> in output, got %q", buf.String())
		}
		if len(stack) != 0 {
			t.Errorf("expected empty stack after matched close, got len=%d", len(stack))
		}
	})

	t.Run("unmatched close is discarded", func(t *testing.T) {
		var buf strings.Builder
		stack := []tagPos{{tag: "b", start: 0}}
		before := buf.Len()
		stack = handleCloseTag("</i>", stack, &buf)
		if buf.Len() != before {
			t.Errorf("unmatched close wrote to buffer: %q", buf.String())
		}
		if len(stack) != 1 {
			t.Errorf("unmatched close should not change stack, got len=%d", len(stack))
		}
	})

	t.Run("interleaved close emits intervening closers and reopens", func(t *testing.T) {
		// Stack: b is open, then i is open. We close b.
		// Expected: </i> emitted (close interleaved), </b> emitted (matched), <i> reopened.
		var buf strings.Builder
		buf.WriteString("<b><i>text")
		stack := []tagPos{
			{tag: "b", start: 3},
			{tag: "i", start: 6},
		}
		stack = handleCloseTag("</b>", stack, &buf)
		out := buf.String()
		if !strings.Contains(out, "</i>") {
			t.Errorf("expected </i> emitted before closing </b>, got: %q", out)
		}
		if !strings.Contains(out, "</b>") {
			t.Errorf("expected </b> emitted, got: %q", out)
		}
		if !strings.Contains(out, "<i>") {
			t.Errorf("expected <i> reopened after closing </b>, got: %q", out)
		}
		// Stack should contain only the reopened <i>
		if len(stack) != 1 || stack[0].tag != "i" {
			t.Errorf("expected stack=[{i}], got %v", stack)
		}
	})
}

// ---------------------------------------------------------------------------
// repairHTMLNesting
// ---------------------------------------------------------------------------

func TestRepairHTMLNesting(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "no tags passthrough",
			in:   "hello world",
			want: "hello world",
		},
		{
			name: "well-formed nesting unchanged",
			in:   "<b><i>text</i></b>",
			want: "<b><i>text</i></b>",
		},
		{
			name: "unclosed tag gets closed at end",
			in:   "<b>hello",
			want: "<b>hello</b>",
		},
		{
			name: "two unclosed tags closed in reverse",
			in:   "<b><i>hello",
			want: "<b><i>hello</i></b>",
		},
		{
			name: "interleaved tags get reordered",
			// Input: <b><i>text</b></i> — i is never properly closed inside b
			// repairHTMLNesting should produce well-formed output
			in:   "<b><i>text</b></i>",
			want: "<b><i>text</i></b><i></i>",
		},
		{
			name: "unmatched close is discarded",
			in:   "hello</b>world",
			// handleCloseTag discards the tag but does not add space; text runs together.
			want: "helloworld",
		},
		{
			name: "self-closing untracked tag passthrough",
			in:   "<br/>text",
			want: "<br/>text",
		},
		{
			name: "unclosed anchor gets closed",
			in:   `<a href="url">click here`,
			want: `<a href="url">click here</a>`,
		},
		{
			name: "code inside pre unclosed",
			in:   "<pre><code>snippet",
			want: "<pre><code>snippet</code></pre>",
		},
		{
			name: "unclosed tag inside text with trailing gt",
			in:   "text <b>bold",
			want: "text <b>bold</b>",
		},
		{
			name: "untracked div not pushed to stack",
			// div is not a Telegram-tracked tag: handleOpenTag writes <div> but does not
			// push it. handleCloseTag then finds no match for </div> and discards it.
			in:   "<div>text</div>",
			want: "<div>text",
		},
		{
			name: "multiple well-formed siblings",
			in:   "<b>a</b><i>b</i>",
			want: "<b>a</b><i>b</i>",
		},
		{
			name: "incomplete tag at end of string",
			in:   "text<b",
			want: "text<b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := repairHTMLNesting(tc.in)
			if got != tc.want {
				t.Errorf("repairHTMLNesting(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// splitMessage
// ---------------------------------------------------------------------------

func TestSplitMessage(t *testing.T) {
	t.Run("short message returned as-is", func(t *testing.T) {
		chunks := splitMessage("hello world", 100)
		if len(chunks) != 1 || chunks[0] != "hello world" {
			t.Errorf("expected single chunk %q, got %v", "hello world", chunks)
		}
	})

	t.Run("exact length returned as-is", func(t *testing.T) {
		msg := strings.Repeat("a", 50)
		chunks := splitMessage(msg, 50)
		if len(chunks) != 1 || chunks[0] != msg {
			t.Errorf("expected single exact-length chunk, got %v", chunks)
		}
	})

	t.Run("split at newline boundary", func(t *testing.T) {
		msg := "first line\nsecond line\nthird line"
		// maxLen = 12 means "first line" fits, newline split
		chunks := splitMessage(msg, 12)
		if len(chunks) < 2 {
			t.Fatalf("expected at least 2 chunks, got %d: %v", len(chunks), chunks)
		}
		// All original content should be preserved across chunks
		combined := strings.Join(chunks, "\n")
		if !strings.Contains(combined, "first line") || !strings.Contains(combined, "second line") {
			t.Errorf("content lost in split: chunks = %v", chunks)
		}
	})

	t.Run("unclosed HTML tag closed at chunk boundary and reopened", func(t *testing.T) {
		// Build a message with a bold tag that spans a split boundary.
		// maxLen = 20; "<b>hello world" is 15 chars, adding more crosses boundary.
		msg := "<b>hello world\nsecond line of bold content</b>"
		chunks := splitMessage(msg, 20)
		if len(chunks) < 2 {
			t.Fatalf("expected split into ≥2 chunks, got %d: %v", len(chunks), chunks)
		}
		// First chunk must close <b>
		if !strings.Contains(chunks[0], "</b>") {
			t.Errorf("first chunk should close <b>: %q", chunks[0])
		}
		// Second chunk must reopen <b>
		if !strings.Contains(chunks[1], "<b>") {
			t.Errorf("second chunk should reopen <b>: %q", chunks[1])
		}
	})

	t.Run("multiple chunks all non-empty", func(t *testing.T) {
		msg := strings.Repeat("line\n", 30)
		chunks := splitMessage(msg, 20)
		for i, ch := range chunks {
			if ch == "" {
				t.Errorf("chunk %d is empty", i)
			}
		}
	})

	t.Run("no newline forces hard split at maxLen", func(t *testing.T) {
		msg := strings.Repeat("a", 50)
		chunks := splitMessage(msg, 20)
		if len(chunks) < 2 {
			t.Fatalf("expected at least 2 chunks for long no-newline content, got %d", len(chunks))
		}
	})
}

// ---------------------------------------------------------------------------
// escapeHTML
// ---------------------------------------------------------------------------

func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"a & b", "a &amp; b"},
		{"<tag>", "&lt;tag&gt;"},
		{"a < b > c & d", "a &lt; b &gt; c &amp; d"},
		{"", ""},
	}
	for _, tc := range tests {
		got := escapeHTML(tc.in)
		if got != tc.want {
			t.Errorf("escapeHTML(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// sanitizeUTF8
// ---------------------------------------------------------------------------

func TestSanitizeUTF8(t *testing.T) {
	t.Run("valid utf8 passthrough", func(t *testing.T) {
		in := "hello, мир"
		if got := sanitizeUTF8(in); got != in {
			t.Errorf("sanitizeUTF8(%q) = %q, want unchanged", in, got)
		}
	})

	t.Run("null byte removed", func(t *testing.T) {
		in := "hel\x00lo"
		got := sanitizeUTF8(in)
		if strings.Contains(got, "\x00") {
			t.Errorf("sanitizeUTF8: null byte not removed: %q", got)
		}
		if got != "hello" {
			t.Errorf("sanitizeUTF8(%q) = %q, want %q", in, got, "hello")
		}
	})

	t.Run("invalid utf8 removed", func(t *testing.T) {
		in := "hello\xff\xfe world"
		got := sanitizeUTF8(in)
		if got == in {
			t.Errorf("sanitizeUTF8: invalid UTF-8 bytes should be removed")
		}
		if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
			t.Errorf("sanitizeUTF8: valid content lost: %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// extractCodeBlocks
// ---------------------------------------------------------------------------

func TestExtractCodeBlocks(t *testing.T) {
	t.Run("no code blocks", func(t *testing.T) {
		result := extractCodeBlocks("plain text")
		if result.text != "plain text" {
			t.Errorf("text changed: %q", result.text)
		}
		if len(result.codes) != 0 {
			t.Errorf("expected no codes, got %v", result.codes)
		}
	})

	t.Run("fenced block extracted with placeholder", func(t *testing.T) {
		in := "before\n```go\nfmt.Println(\"hello\")\n```\nafter"
		result := extractCodeBlocks(in)
		if strings.Contains(result.text, "```") {
			t.Errorf("code block not replaced: %q", result.text)
		}
		if !strings.Contains(result.text, "\x00CB0\x00") {
			t.Errorf("placeholder not present: %q", result.text)
		}
		if len(result.codes) != 1 {
			t.Fatalf("expected 1 code, got %d", len(result.codes))
		}
		if result.langs[0] != "go" {
			t.Errorf("lang: got %q, want %q", result.langs[0], "go")
		}
		if !strings.Contains(result.codes[0], "Println") {
			t.Errorf("code content not preserved: %q", result.codes[0])
		}
	})

	t.Run("two code blocks get sequential placeholders", func(t *testing.T) {
		in := "```\nblock1\n```\ntext\n```python\nblock2\n```"
		result := extractCodeBlocks(in)
		if !strings.Contains(result.text, "\x00CB0\x00") {
			t.Errorf("CB0 placeholder missing: %q", result.text)
		}
		if !strings.Contains(result.text, "\x00CB1\x00") {
			t.Errorf("CB1 placeholder missing: %q", result.text)
		}
		if len(result.codes) != 2 {
			t.Errorf("expected 2 codes, got %d", len(result.codes))
		}
		if result.langs[1] != "python" {
			t.Errorf("second block lang: got %q, want %q", result.langs[1], "python")
		}
	})

	t.Run("block without language tag", func(t *testing.T) {
		in := "```\ncode here\n```"
		result := extractCodeBlocks(in)
		if result.langs[0] != "" {
			t.Errorf("expected empty lang, got %q", result.langs[0])
		}
	})
}

// ---------------------------------------------------------------------------
// extractInlineCodes
// ---------------------------------------------------------------------------

func TestExtractInlineCodes(t *testing.T) {
	t.Run("no inline codes passthrough", func(t *testing.T) {
		result := extractInlineCodes("plain text")
		if result.text != "plain text" {
			t.Errorf("text changed: %q", result.text)
		}
		if len(result.codes) != 0 {
			t.Errorf("expected no codes, got %v", result.codes)
		}
	})

	t.Run("single inline code replaced", func(t *testing.T) {
		in := "Use `fmt.Println` here"
		result := extractInlineCodes(in)
		if strings.Contains(result.text, "`") {
			t.Errorf("backtick not removed: %q", result.text)
		}
		if !strings.Contains(result.text, "\x00IC0\x00") {
			t.Errorf("IC0 placeholder missing: %q", result.text)
		}
		if len(result.codes) != 1 || result.codes[0] != "fmt.Println" {
			t.Errorf("code not preserved: %v", result.codes)
		}
	})

	t.Run("two inline codes get sequential placeholders", func(t *testing.T) {
		in := "Use `a` and `b`"
		result := extractInlineCodes(in)
		if !strings.Contains(result.text, "\x00IC0\x00") || !strings.Contains(result.text, "\x00IC1\x00") {
			t.Errorf("placeholders missing: %q", result.text)
		}
		if len(result.codes) != 2 {
			t.Errorf("expected 2 codes, got %d", len(result.codes))
		}
	})
}

// ---------------------------------------------------------------------------
// convertBlockquotes
// ---------------------------------------------------------------------------

func TestConvertBlockquotes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single line blockquote",
			in:   "&gt; hello",
			want: "<blockquote>hello</blockquote>\n",
		},
		{
			name: "blockquote with space prefix",
			in:   "&gt; line one",
			want: "<blockquote>line one</blockquote>\n",
		},
		{
			name: "no blockquote passthrough",
			in:   "plain text",
			want: "plain text",
		},
		{
			name: "multi-line blockquote",
			in:   "&gt; line one\n&gt; line two",
			want: "<blockquote>line one\nline two</blockquote>\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := convertBlockquotes(tc.in)
			if got != tc.want {
				t.Errorf("convertBlockquotes(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CompactForTelegram
// ---------------------------------------------------------------------------

func TestCompactForTelegram(t *testing.T) {
	t.Run("short text passthrough", func(t *testing.T) {
		in := "short"
		got := CompactForTelegram(in, 100)
		if got != in {
			t.Errorf("expected passthrough, got %q", got)
		}
	})

	t.Run("exact length passthrough", func(t *testing.T) {
		in := strings.Repeat("a", 100)
		got := CompactForTelegram(in, 100)
		if got != in {
			t.Errorf("expected passthrough at exact length")
		}
	})

	t.Run("large code block stripped", func(t *testing.T) {
		// Build a code block > 500 chars
		code := strings.Repeat("x", 510)
		in := "header\n```go\n" + code + "\n```\nfooter"
		// maxChars large enough that only code block stripping is needed
		maxChars := len(in) - 10
		got := CompactForTelegram(in, maxChars)
		if strings.Contains(got, code) {
			t.Errorf("large code block should be stripped")
		}
		if !strings.Contains(got, "(code block trimmed)") {
			t.Errorf("trimmed placeholder missing in: %q", got)
		}
	})

	t.Run("verbose section heading truncates", func(t *testing.T) {
		// Put verbose heading after 200+ chars so the loc[0] > 200 condition triggers.
		// Total length must exceed maxChars to enter the function body.
		prefix := strings.Repeat("a", 250)
		verboseBody := strings.Repeat("log line\n", 50) // ~450 chars — makes total >> maxChars
		in := prefix + "\n# Raw Logs\n" + verboseBody
		// maxChars = 400: text is ~700 chars so we enter the function,
		// code-block strip won't help (no code blocks), verbose section at byte 251 > 200.
		got := CompactForTelegram(in, 400)
		if strings.Contains(got, "Raw Logs") {
			t.Errorf("verbose section should be truncated, got: %q", got)
		}
		if !strings.Contains(got, "_(truncated)_") {
			t.Errorf("truncated marker missing in: %q", got)
		}
	})

	t.Run("hard truncate at newline boundary", func(t *testing.T) {
		// No verbose sections; text longer than maxChars
		in := strings.Repeat("line\n", 60) // 300 chars
		got := CompactForTelegram(in, 100)
		if len(got) > 150 { // 100 - 30 + len("... (truncated)") overhead
			t.Errorf("result too long: %d chars", len(got))
		}
		if !strings.Contains(got, "_(truncated)_") {
			t.Errorf("truncated marker missing")
		}
	})
}

// ---------------------------------------------------------------------------
// stripMarkdown
// ---------------------------------------------------------------------------

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bold stripped",
			in:   "**bold**",
			want: "bold",
		},
		{
			name: "italic stripped",
			in:   "*italic*",
			want: "italic",
		},
		{
			name: "heading hash stripped",
			in:   "# Title",
			want: "Title",
		},
		{
			name: "link formatted",
			in:   "[text](url)",
			want: "text (url)",
		},
		{
			name: "inline code stripped",
			in:   "`code`",
			want: "code",
		},
		{
			name: "strikethrough stripped",
			in:   "~~text~~",
			want: "text",
		},
		{
			name: "triple backtick removed",
			// reStripCodeFence removes leading ``` with optional newline: "```\n" → ""
			// leaving "code\n```"; then strings.ReplaceAll removes trailing "```" → "code\n"
			in:   "```\ncode\n```",
			want: "code\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMarkdown(tc.in)
			if got != tc.want {
				t.Errorf("stripMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// markdownToTelegramHTML (integration-style)
// ---------------------------------------------------------------------------

func TestMarkdownToTelegramHTML(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantContain string
		wantAbsent  string
	}{
		{
			name:        "empty returns empty",
			in:          "",
			wantContain: "",
		},
		{
			name:        "bold markdown to HTML",
			in:          "**hello**",
			wantContain: "<b>hello</b>",
		},
		{
			name:        "italic star to HTML",
			in:          "*world*",
			wantContain: "<i>world</i>",
		},
		{
			name:        "heading to bold",
			in:          "# Title Here",
			wantContain: "<b>Title Here</b>",
		},
		{
			name:        "link to anchor",
			in:          "[click](https://example.com)",
			wantContain: `<a href="https://example.com">click</a>`,
		},
		{
			name:        "inline code protected from bold conversion",
			in:          "`**not bold**`",
			wantContain: "<code>",
			wantAbsent:  "<b>",
		},
		{
			name:        "code block with language",
			in:          "```go\nfmt.Println()\n```",
			wantContain: `class="language-go"`,
		},
		{
			name:        "ampersand escaped",
			in:          "a & b",
			wantContain: "a &amp; b",
		},
		{
			name:        "strikethrough",
			in:          "~~struck~~",
			wantContain: "<s>struck</s>",
		},
		{
			name:        "bold-italic triple star",
			in:          "***both***",
			wantContain: "<b><i>both</i></b>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := markdownToTelegramHTML(tc.in)
			if tc.wantContain != "" && !strings.Contains(got, tc.wantContain) {
				t.Errorf("markdownToTelegramHTML(%q) = %q\n  want to contain: %q", tc.in, got, tc.wantContain)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("markdownToTelegramHTML(%q) = %q\n  should NOT contain: %q", tc.in, got, tc.wantAbsent)
			}
		})
	}
}
