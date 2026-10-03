package telegram

import (
	"sort"
	"strings"
	"unicode"
)

// Term is one glossary entry.
type Term struct {
	Canonical string   // canonical spelling, e.g. "HeadHunter"
	Aliases   []string // STT-garbled spoken forms to normalize, e.g. {"хэт хантер","хед хантер"}
	Bold      bool     // when true, wrap each normalized occurrence of Canonical in <b>…</b>
}

// glossaryEntry is a compiled alias: its lowercased words (for case-insensitive
// matching) and the term to emit on a match.
type glossaryEntry struct {
	words     []string // alias split into lowercase words
	wordRunes [][]rune // precomputed []rune of each word (hot-path: avoids re-splitting per match)
	term      Term
}

// Glossary normalizes STT-garbled brand/service/person names to their canonical
// spelling and optionally bolds them. Compile once with NewGlossary; the result
// is immutable and safe for concurrent use by any number of Apply calls.
type Glossary struct {
	entries []glossaryEntry // sorted longest-first so the longest alias wins
}

// NewGlossary compiles a set of terms into a matcher. The Canonical spelling of
// each term is also registered as an alias of itself, so an already-correct but
// wrong-case occurrence (e.g. "headhunter") is normalized to the canonical
// spelling (e.g. "HeadHunter"). nil/empty terms yield a no-op Glossary.
func NewGlossary(terms []Term) *Glossary {
	g := &Glossary{}
	for _, t := range terms {
		if t.Canonical == "" {
			continue
		}
		seen := map[string]bool{}
		add := func(alias string) {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				return
			}
			words := splitAliasWords(alias)
			if len(words) == 0 {
				return
			}
			key := strings.Join(words, "\x00")
			if seen[key] {
				return
			}
			seen[key] = true
			wr := make([][]rune, len(words))
			for k, w := range words {
				wr[k] = []rune(w)
			}
			g.entries = append(g.entries, glossaryEntry{words: words, wordRunes: wr, term: t})
		}
		for _, a := range t.Aliases {
			add(a)
		}
		// Register the canonical itself so wrong-case occurrences are normalized.
		add(t.Canonical)
	}
	// Longest alias first: compare by total rune length of the alias, then by
	// the raw alias text for a deterministic order. This guarantees that when
	// one alias is a prefix/subset of another ("чат" vs "чат джи пи ти"), the
	// longer one is tried first and wins.
	sort.SliceStable(g.entries, func(i, j int) bool {
		li, lj := entryRuneLen(g.entries[i]), entryRuneLen(g.entries[j])
		if li != lj {
			return li > lj
		}
		return aliasText(g.entries[i]) < aliasText(g.entries[j])
	})
	return g
}

// Apply normalizes every glossary term occurrence in text to its canonical
// spelling, optionally bolding it. Matching is case-insensitive with
// case-correct output, Unicode/Cyrillic word-boundary aware (whole words only,
// never a substring inside a longer word), and tolerant of run-of-whitespace
// between the words of a multi-word alias. Existing HTML tags are passed
// through untouched, and a bold term is not double-wrapped if it is already
// inside a <b>…</b> span. A nil Glossary is a no-op.
func (g *Glossary) Apply(text string) string {
	if g == nil || len(g.entries) == 0 || text == "" {
		return text
	}
	runes := []rune(text)
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	bDepth := 0 // open <b> nesting depth (for double-wrap suppression)
	for i < len(runes) {
		r := runes[i]
		// Pass HTML tags through verbatim; track <b>/</b> for double-wrap guard.
		// Only treat '<' as a tag start when the next rune is a letter or '/'
		// (i.e. <b>, </b>, <a …>). A stray '<' (e.g. "темп < 5", a lone trailing
		// '<') is emitted literally and the normal walk continues — it must NOT
		// swallow following text as a fake tag or break the loop.
		if r == '<' && i+1 < len(runes) && (unicode.IsLetter(runes[i+1]) || runes[i+1] == '/') {
			end := indexRune(runes, i, '>')
			if end < 0 {
				b.WriteRune('<')
				i++
				continue
			}
			end++ // include '>'
			tag := strings.ToLower(strings.TrimSpace(string(runes[i+1 : end-1])))
			tag = strings.TrimSuffix(tag, "/")
			name := tagName(tag)
			switch name {
			case "b", "strong":
				if !strings.HasPrefix(tag, "/") {
					bDepth++
				} else {
					if bDepth > 0 {
						bDepth--
					}
				}
			}
			b.WriteString(string(runes[i:end]))
			i = end
			continue
		}
		if r == '<' {
			// Stray '<' (not followed by letter/'/', or trailing with no next
			// rune): emit literally and continue the normal walk.
			b.WriteRune('<')
			i++
			continue
		}
		// Only attempt a match at a word start.
		if isWordRune(r) && (i == 0 || !isWordRune(runes[i-1])) {
			if end, term, ok := g.matchAt(runes, i); ok {
				emitTerm(&b, term, bDepth)
				i = end
				continue
			}
			// No match — copy the whole word run unchanged.
			j := i
			for j < len(runes) && isWordRune(runes[j]) {
				j++
			}
			b.WriteString(string(runes[i:j]))
			i = j
			continue
		}
		b.WriteRune(r)
		i++
	}
	return b.String()
}

// matchAt tries to match any compiled alias starting at runes[pos], which must
// be a word start. Returns the end position (exclusive) and the winning term.
func (g *Glossary) matchAt(runes []rune, pos int) (int, Term, bool) {
	for _, e := range g.entries {
		if end, ok := matchAlias(runes, pos, e.wordRunes); ok {
			return end, e.term, true
		}
	}
	return 0, Term{}, false
}

// matchAlias reports whether the input at pos matches the alias word sequence,
// tolerating any non-empty run of whitespace between alias words. The match
// must be followed by a word boundary (non-word rune or end of input).
// words is the precomputed [][]rune of each alias word (lowercased).
func matchAlias(runes []rune, pos int, words [][]rune) (int, bool) {
	i := pos
	for k, wr := range words {
		// Must be at a word rune.
		if i >= len(runes) || !isWordRune(runes[i]) {
			return 0, false
		}
		if i+len(wr) > len(runes) {
			return 0, false
		}
		for c := 0; c < len(wr); c++ {
			if unicode.ToLower(runes[i]) != unicode.ToLower(wr[c]) {
				return 0, false
			}
			i++
		}
		if k == len(words)-1 {
			// Last word: require a word boundary after.
			if i < len(runes) && isWordRune(runes[i]) {
				return 0, false
			}
			return i, true
		}
		// Between alias words: require at least one whitespace, then next word.
		if i >= len(runes) || !unicode.IsSpace(runes[i]) {
			return 0, false
		}
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
	}
	return i, true
}

// emitTerm writes the canonical spelling, bold-wrapped if requested and not
// already inside a <b> span (bDepth > 0).
func emitTerm(b *strings.Builder, t Term, bDepth int) {
	if t.Bold && bDepth == 0 {
		b.WriteString("<b>")
		b.WriteString(t.Canonical)
		b.WriteString("</b>")
		return
	}
	b.WriteString(t.Canonical)
}

// splitAliasWords lowercases an alias and splits it into words on runs of
// whitespace, dropping empty segments.
func splitAliasWords(alias string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range alias {
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return words
}

// isWordRune reports whether r is a word character (Unicode letter or digit).
// Used for Cyrillic-safe word boundaries — Go regexp \b is ASCII-only and
// would break on Cyrillic, so we avoid it.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// indexRune returns the index of the first occurrence of c in runes[start:],
// as an absolute index into runes, or -1 if not found.
func indexRune(runes []rune, start int, c rune) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == c {
			return i
		}
	}
	return -1
}

// tagName extracts the tag name from a normalized (lowercased, trimmed) tag
// content string, handling a leading "/" for closing tags.
// NOTE: parallels html.go's parseTagName but operates on already-normalized
// content; intentionally not unified to keep glossary self-contained.
func tagName(tag string) string {
	s := strings.TrimPrefix(tag, "/")
	if sp := strings.IndexByte(s, ' '); sp > 0 {
		return s[:sp]
	}
	return s
}

// entryRuneLen returns the total number of runes across all words of an entry.
func entryRuneLen(e glossaryEntry) int {
	n := 0
	for _, w := range e.words {
		n += len([]rune(w))
	}
	return n
}

// aliasText reconstructs a space-joined lowercased alias text for sorting.
func aliasText(e glossaryEntry) string {
	return strings.Join(e.words, " ")
}
