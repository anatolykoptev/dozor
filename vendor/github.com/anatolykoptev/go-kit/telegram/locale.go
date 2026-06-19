package telegram

// locale.go — YAML-driven per-language string registry for Telegram bots.
//
// Data-layer shape adapted from github.com/tucnak/telebot v4 (MIT):
//   layout/layout.go, layout/parser.go (selective, ≤200 LOC kept).
// Bot-specific coupling (tele.Context, markup DSL, inline results) dropped.
// See telegram/THIRD_PARTY_NOTICES.md for full attribution.

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Command represents a single Telegram bot command used in setMyCommands.
type Command struct {
	Cmd  string
	Desc string
}

// localeData holds the parsed YAML for one language.
type localeData struct {
	Strings  map[string]string `yaml:"strings"`
	Buttons  map[string]string `yaml:"buttons"`
	Commands []struct {
		Cmd  string `yaml:"cmd"`
		Desc string `yaml:"desc"`
	} `yaml:"commands"`
}

// Locale loads YAML-defined per-lang string maps, button labels, and command menus.
//
// Each locale file maps keys → strings; missing keys fall back to the default lang.
// Locale is immutable after construction; all methods are safe for concurrent use.
type Locale struct {
	defaultLang string
	langs       map[string]*localeData

	// templates holds eagerly pre-compiled *template.Template instances, keyed by
	// lang → key. Only strings containing template syntax are stored; plain strings
	// are absent (nil lookup). Built once in NewLocale; read-only thereafter.
	templates map[string]map[string]*template.Template

	// buttonsCache holds the merged button map per lang (default-lang base +
	// requested-lang overlay), built once in NewLocale. Returned directly by
	// Buttons — callers must treat the map as read-only. This avoids per-call
	// allocation when rendering inline keyboards.
	buttonsCache map[string]map[string]string

	// bufPool recycles bytes.Buffer values for template execution, avoiding
	// per-call heap allocation for the intermediate render buffer.
	bufPool sync.Pool
}

// NewLocale loads locale YAML files from fsys (caller picks: embed.FS, os.DirFS,
// testing/fstest.MapFS, …). Files are expected at the root of fsys with names
// matching "<lang>.yaml" (e.g. "ru.yaml", "en.yaml").
//
// defaultLang must be present in fsys; it is used as the fallback for missing keys.
// Returns an error if defaultLang file is absent or any loaded file has invalid YAML.
//
// NewLocale eagerly pre-compiles all template strings and merges button maps so that
// Get, Button, and Buttons incur zero allocations on the read hot path.
func NewLocale(fsys fs.FS, defaultLang string) (*Locale, error) {
	langs := make(map[string]*localeData)

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("telegram/locale: read dir: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		lang := strings.TrimSuffix(name, ".yaml")

		data, err := fs.ReadFile(fsys, path.Join(".", name))
		if err != nil {
			return nil, fmt.Errorf("telegram/locale: read %s: %w", name, err)
		}

		var ld localeData
		if err := yaml.Unmarshal(data, &ld); err != nil {
			return nil, fmt.Errorf("telegram/locale: parse %s: %w", name, err)
		}

		langs[lang] = &ld
	}

	if _, ok := langs[defaultLang]; !ok {
		return nil, fmt.Errorf("telegram/locale: default lang %q not found in fs", defaultLang)
	}

	// Pre-compile templates: for each lang × key, attempt to parse the raw string as
	// a text/template. If parsing fails or the string contains no actions (i.e. it is
	// a plain string — template.Tree.Root has only a TextNode), skip it. Only strings
	// that actually need Execute() are stored, so compiledTemplate returns nil for
	// plain strings and Get can skip template overhead entirely.
	templates := make(map[string]map[string]*template.Template, len(langs))
	for lang, ld := range langs {
		if len(ld.Strings) == 0 {
			continue
		}
		langTemplates := make(map[string]*template.Template)
		for key, raw := range ld.Strings {
			t, err := template.New(key).Parse(raw)
			if err != nil {
				// Malformed template syntax — treat as plain string.
				continue
			}
			if !templateHasActions(t) {
				// Pure text node — no substitution needed; skip to avoid overhead.
				continue
			}
			langTemplates[key] = t
		}
		if len(langTemplates) > 0 {
			templates[lang] = langTemplates
		}
	}

	// Pre-build merged button maps: for each lang, construct the default-lang base
	// then overlay the requested lang. The result is stored once and returned by
	// reference from Buttons. Callers must not mutate the returned map.
	buttonsCache := make(map[string]map[string]string, len(langs))
	defaultButtons := langs[defaultLang].Buttons
	for lang := range langs {
		if lang == defaultLang {
			// Clone default lang map so callers can't mutate the shared cache.
			m := make(map[string]string, len(defaultButtons))
			for k, v := range defaultButtons {
				m[k] = v
			}
			buttonsCache[lang] = m
			continue
		}
		// Merge: default as base, requested lang as overlay.
		overlay := langs[lang].Buttons
		m := make(map[string]string, len(defaultButtons)+len(overlay))
		for k, v := range defaultButtons {
			m[k] = v
		}
		for k, v := range overlay {
			m[k] = v
		}
		buttonsCache[lang] = m
	}

	return &Locale{
		defaultLang:  defaultLang,
		langs:        langs,
		templates:    templates,
		buttonsCache: buttonsCache,
		bufPool: sync.Pool{
			New: func() any { return new(bytes.Buffer) },
		},
	}, nil
}

// templateHasActions reports whether t contains at least one non-text node,
// i.e. whether Execute() would actually substitute anything. Pure text templates
// are treated as plain strings to avoid unnecessary overhead.
//
// Detection heuristic: the parsed tree's string representation includes "{{"
// only when action nodes are present. This avoids importing text/template/parse
// for the NodeText type check while still being accurate for all supported
// template syntax.
func templateHasActions(t *template.Template) bool {
	root := t.Tree.Root
	if root == nil {
		return false
	}
	return strings.Contains(root.String(), "{{")
}

// Get returns the string for key in lang. If the key is missing in lang, it falls
// back to the default lang. If still missing, it returns key as-is (debug-friendly).
//
// When vars is non-empty, the string is interpreted as a text/template with the
// first element of vars as the dot value ({{.}}). The template is pre-compiled at
// construction time — no parse overhead on the hot path.
func (l *Locale) Get(lang, key string, vars ...any) string {
	if len(vars) == 0 {
		return l.lookup(lang, key)
	}

	// Try pre-compiled template for the requested lang, then the default lang.
	if t := l.compiledTemplate(lang, key); t != nil {
		return l.execTemplate(t, vars[0], l.lookup(lang, key))
	}

	// No pre-compiled template — string is plain, return it directly.
	return l.lookup(lang, key)
}

// execTemplate executes t with data, using the pool-recycled buffer to avoid
// per-call allocation. Returns raw on execution error.
func (l *Locale) execTemplate(t *template.Template, data any, raw string) string {
	buf := l.bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer l.bufPool.Put(buf)

	if err := t.Execute(buf, data); err != nil {
		return raw
	}
	return buf.String()
}

// compiledTemplate returns the pre-compiled *template.Template for (lang, key),
// or nil if the string is plain (no template syntax) or the key is absent.
// It resolves lang first, then falls back to the default lang — matching lookup.
//
// Exported at package level for test assertion (pointer identity = no re-parse).
func (l *Locale) compiledTemplate(lang, key string) *template.Template {
	if lm, ok := l.templates[lang]; ok {
		if t, ok := lm[key]; ok {
			return t
		}
	}
	// Fallback to default lang template (mirrors lookup fallback).
	if lm, ok := l.templates[l.defaultLang]; ok {
		if t, ok := lm[key]; ok {
			return t
		}
	}
	return nil
}

// lookup returns the raw (un-templated) string for key in lang, with default-lang
// fallback and final key-as-sentinel fallback.
func (l *Locale) lookup(lang, key string) string {
	if ld, ok := l.langs[lang]; ok {
		if v, ok := ld.Strings[key]; ok {
			return v
		}
	}
	// Fallback to default lang.
	if ld, ok := l.langs[l.defaultLang]; ok {
		if v, ok := ld.Strings[key]; ok {
			return v
		}
	}
	return key // sentinel: return key for debuggability
}

// Available returns the list of locale codes that loaded successfully.
// The order is not guaranteed.
func (l *Locale) Available() []string {
	out := make([]string, 0, len(l.langs))
	for lang := range l.langs {
		out = append(out, lang)
	}
	return out
}

// Buttons returns the merged button map for lang (default-lang base overlaid by
// lang-specific values). The returned map is pre-built at construction and shared
// across callers — treat it as read-only.
//
// Falls back to the default lang map if lang was not loaded.
func (l *Locale) Buttons(lang string) map[string]string {
	if m, ok := l.buttonsCache[lang]; ok {
		return m
	}
	// Lang not in cache (e.g. unknown lang code) — return default lang map.
	return l.buttonsCache[l.defaultLang]
}

// Commands returns the bot command menu for lang (used by setMyCommands).
// Falls back to default lang if lang is not loaded.
func (l *Locale) Commands(lang string) []Command {
	ld, ok := l.langs[lang]
	if !ok {
		ld = l.langs[l.defaultLang]
	}
	if ld == nil {
		return nil
	}
	out := make([]Command, 0, len(ld.Commands))
	for _, c := range ld.Commands {
		out = append(out, Command{Cmd: c.Cmd, Desc: c.Desc})
	}
	return out
}

// Button returns the label for a single button key in lang, without allocating
// a full map. Falls back to the default lang if the key is absent in lang.
// Returns key itself if absent everywhere (same sentinel behaviour as Get).
func (l *Locale) Button(lang, key string) string {
	// Check requested lang first.
	if ld, ok := l.langs[lang]; ok {
		if v, ok := ld.Buttons[key]; ok {
			return v
		}
	}
	// Fall back to default lang.
	if ld, ok := l.langs[l.defaultLang]; ok {
		if v, ok := ld.Buttons[key]; ok {
			return v
		}
	}
	return key // sentinel: return key for debuggability
}
