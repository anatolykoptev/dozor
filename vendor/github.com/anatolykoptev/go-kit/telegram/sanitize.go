// Package telegram provides SDK-agnostic Telegram formatting utilities.
package telegram

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// allowedAnchorSchemes are the URL schemes allowed in <a href>.
var allowedAnchorSchemes = []string{"http://", "https://", "tg://", "mailto:"}

// dropTags are tags whose content is dropped entirely (security).
var dropTags = map[string]bool{
	"script": true, "style": true, "iframe": true,
	"object": true, "embed": true, "form": true,
	"input": true, "button": true,
}

// blockWrapTags are div-like tags: emit content + "\n".
var blockWrapTags = map[string]bool{
	"div": true, "section": true, "article": true,
	"header": true, "footer": true, "main": true,
	"nav": true, "aside": true,
}

// passThruTags are Telegram-native tags kept as-is.
var passThruTags = map[string]bool{
	"b": true, "i": true, "u": true, "s": true,
	"code": true, "pre": true, "blockquote": true,
}

// synonymTags map HTML synonyms to Telegram tag names.
var synonymTags = map[string]string{
	"strong": "b",
	"em":     "i",
	"ins":    "u",
	"strike": "s",
	"del":    "s",
}

// SanitizeHTML converts arbitrary HTML into Telegram-safe HTML using the
// golang.org/x/net/html parser (not regex). Synonyms are renamed (strong→b,
// em→i, etc.), block elements are converted to newlines, lists produce
// bullets/numbers, unknown tags are stripped keeping their text content,
// and dangerous tags (script, style, iframe) are dropped entirely.
func SanitizeHTML(input string) string {
	if input == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		// Fallback: return escaped input so nothing dangerous leaks.
		return EscapeHTML(input)
	}

	var b strings.Builder
	b.Grow(len(input))
	walkNode(&b, doc, nil)
	return b.String()
}

// walkNode recursively walks an html.Node tree, writing Telegram-safe HTML
// to b. parentTag tracks the enclosing list tag ("ol"/"ul") for li rendering.
func walkNode(b *strings.Builder, n *html.Node, listCtx *listContext) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(EscapeHTML(n.Data))
	case html.ElementNode:
		renderElement(b, n, listCtx)
	default:
		// Document, doctype, comment — walk children.
		walkChildren(b, n, listCtx)
	}
}

// walkChildren walks all children of n.
func walkChildren(b *strings.Builder, n *html.Node, listCtx *listContext) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNode(b, c, listCtx)
	}
}

// listContext carries numbering state for ordered lists.
type listContext struct {
	ordered bool
	counter int
}

// renderElement dispatches element rendering based on tag name.
func renderElement(b *strings.Builder, n *html.Node, listCtx *listContext) {
	tag := n.Data
	if dropTags[tag] {
		return // drop tag + all content
	}
	if renderInlineElement(b, n, tag, listCtx) {
		return
	}
	renderBlockElement(b, n, tag, listCtx)
}

// renderInlineElement handles void, inline, and special-attribute elements.
// Returns true if the tag was handled.
func renderInlineElement(b *strings.Builder, n *html.Node, tag string, listCtx *listContext) bool {
	switch tag {
	case "br":
		b.WriteString("\n")
	case "hr":
		b.WriteString("\n———\n")
	case "img":
		renderImg(b, n)
	case "a":
		renderAnchor(b, n, listCtx)
	case "span":
		renderSpan(b, n, listCtx)
	case "code":
		renderCode(b, n, listCtx)
	case "tg-spoiler":
		renderPassThru(b, n, listCtx, "tg-spoiler")
	default:
		if passThruTags[tag] {
			renderPassThru(b, n, listCtx, tag)
			return true
		}
		if syn := synonymTags[tag]; syn != "" {
			renderPassThru(b, n, listCtx, syn)
			return true
		}
		return false
	}
	return true
}

// renderBlockElement handles block-level elements (lists, tables, headings, wrappers).
func renderBlockElement(b *strings.Builder, n *html.Node, tag string, listCtx *listContext) {
	switch tag {
	case "p":
		walkChildren(b, n, listCtx)
		b.WriteString("\n\n")
	case "ul":
		walkChildren(b, n, &listContext{ordered: false})
	case "ol":
		walkChildren(b, n, &listContext{ordered: true})
	case "li":
		renderListItem(b, n, listCtx)
	case "table", "thead", "tbody", "tr", "td", "th":
		renderTableElement(b, n, tag, listCtx)
	default:
		if isHeading(tag) {
			b.WriteString("<b>")
			walkChildren(b, n, listCtx)
			b.WriteString("</b>\n")
			return
		}
		if blockWrapTags[tag] {
			walkChildren(b, n, listCtx)
			b.WriteString("\n")
			return
		}
		// Unknown tag — strip wrapper, keep content.
		walkChildren(b, n, listCtx)
	}
}

// renderPassThru writes an element with its original tag name (renamed or as-is)
// but drops all attributes (caller is responsible for attribute logic).
func renderPassThru(b *strings.Builder, n *html.Node, listCtx *listContext, outTag string) {
	b.WriteString("<")
	b.WriteString(outTag)
	b.WriteString(">")
	walkChildren(b, n, listCtx)
	b.WriteString("</")
	b.WriteString(outTag)
	b.WriteString(">")
}

// renderAnchor writes <a href="..."> keeping only safe href, or strips to text.
// If href contains a literal quote character (which would indicate an entity-
// decoded injection attempt like &quot; inside the attribute value), the link
// is stripped to its text content rather than emitted.
func renderAnchor(b *strings.Builder, n *html.Node, listCtx *listContext) {
	href := attrVal(n, "href")
	if href == "" || !isSafeHref(href) || strings.ContainsAny(href, `"'`) {
		walkChildren(b, n, listCtx)
		return
	}
	b.WriteString(`<a href="`)
	b.WriteString(html.EscapeString(href))
	b.WriteString(`">`)
	walkChildren(b, n, listCtx)
	b.WriteString("</a>")
}

// renderSpan either keeps <span class="tg-spoiler"> or strips to text.
func renderSpan(b *strings.Builder, n *html.Node, listCtx *listContext) {
	cls := attrVal(n, "class")
	if cls == "tg-spoiler" {
		b.WriteString(`<span class="tg-spoiler">`)
		walkChildren(b, n, listCtx)
		b.WriteString("</span>")
		return
	}
	walkChildren(b, n, listCtx)
}

// renderCode keeps <code class="language-XYZ"> only; other class attrs stripped.
func renderCode(b *strings.Builder, n *html.Node, listCtx *listContext) {
	cls := attrVal(n, "class")
	if strings.HasPrefix(cls, "language-") {
		b.WriteString(`<code class="`)
		b.WriteString(cls)
		b.WriteString(`">`)
		walkChildren(b, n, listCtx)
		b.WriteString("</code>")
		return
	}
	b.WriteString("<code>")
	walkChildren(b, n, listCtx)
	b.WriteString("</code>")
}

// renderImg writes the alt text or "[image]" placeholder.
func renderImg(b *strings.Builder, n *html.Node) {
	alt := attrVal(n, "alt")
	if alt == "" {
		b.WriteString("[image]")
	} else {
		b.WriteString("[")
		b.WriteString(alt)
		b.WriteString("]")
	}
}

// renderListItem writes a bullet or numbered list entry.
func renderListItem(b *strings.Builder, n *html.Node, listCtx *listContext) {
	if listCtx == nil || !listCtx.ordered {
		b.WriteString("• ")
	} else {
		listCtx.counter++
		b.WriteString(strconv.Itoa(listCtx.counter))
		b.WriteString(". ")
	}
	walkChildren(b, n, listCtx)
	b.WriteString("\n")
}

// renderTableElement handles table-related tags with best-effort formatting.
func renderTableElement(b *strings.Builder, n *html.Node, tag string, listCtx *listContext) {
	switch tag {
	case "td", "th":
		walkChildren(b, n, listCtx)
		b.WriteString("\t")
	case "tr":
		walkChildren(b, n, listCtx)
		b.WriteString("\n")
	default: // table, thead, tbody
		walkChildren(b, n, listCtx)
	}
}

// isHeading returns true for h1–h6.
func isHeading(tag string) bool {
	if len(tag) != 2 || tag[0] != 'h' { //nolint:mnd // length 2: "h" + digit
		return false
	}
	d := tag[1]
	return d >= '1' && d <= '6'
}

// attrVal returns the value of attribute key from an element node.
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// isSafeHref returns true if href starts with an allowed scheme.
// Scheme detection is case-insensitive; the href value is returned unmodified.
func isSafeHref(href string) bool {
	lower := strings.ToLower(href)
	for _, scheme := range allowedAnchorSchemes {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	return false
}

