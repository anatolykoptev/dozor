// Package tgsafe keeps Telegram bot tokens out of error values and logs.
//
// The Bot API puts the credential in the URL path
// (https://api.telegram.org/bot<id>:<secret>/method, and /file/bot<id>:<secret>/
// for downloads). net/http builds a *url.Error, whose Error() prints the full
// URL, only AFTER Transport.RoundTrip returns, inside http.Client.Do, so the
// only code that can scrub it is the caller of Do: wrap the client
// ([NewHTTPClient]), not the transport. tgbotapi's GetUpdatesChan also prints
// every failed poll through its package logger, so install [NewLogger] with
// the SDK's SetLogger.
//
// The package does not import any Telegram SDK: [HTTPClient] satisfies the
// HTTPClient interface and [Logger] the BotLogger interface of
// github.com/go-telegram-bot-api/telegram-bot-api/v5 and of the
// github.com/OvyFlash/telegram-bot-api fork alike. For the OvyFlash fork,
// telegram/tgapi5.NewSafeBotAPI wires everything in one call.
//
// Consumers of go-telegram-bot-api/v5 (not the OvyFlash fork), which
// tgapi5.NewSafeBotAPI does not cover, wire the same pieces by hand:
//
//	hc := tgsafe.NewHTTPClient(&http.Client{Timeout: 75 * time.Second}, token)
//	bot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, hc)
//	_ = tgbotapi.SetLogger(tgsafe.NewLogger(slog.Default(), token)) // once, at startup
//
// Use the same hc (or one built the same way) for file downloads: a file link
// from tgFile.Link(token) embeds the token too.
package tgsafe

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Placeholder replaces a literal secret.
const Placeholder = "[REDACTED]"

// botSegmentPlaceholder replaces a whole bot<id>:<secret> path segment.
const botSegmentPlaceholder = "bot<redacted>"

// botSegmentRE matches the credential segment of a Bot API URL
// ("bot123456789:AA..." in /bot<token>/method and /file/bot<token>/path). It
// works without knowing the token, so a rotated token, or one this process
// never saw, is still scrubbed.
//
// The separator is ":" or its query-escaped forms "%3A"/"%3a" (the token inside
// a URL-encoded parameter) and "%253A"/"%253a" (escaped twice, e.g. a URL
// carried as a query value of another URL).
//
// Bounds: Telegram bot ids are numeric and today 8-10 digits, secrets are 35
// characters of [A-Za-z0-9_-]. \d{6,} and {20,} sit well below those so a
// token of any real size matches, while ordinary text such as "bot42: ready"
// or a short "bot123456:abc" does not turn into a redaction. A token that
// falls outside the bounds is still removed when passed as a literal secret.
var botSegmentRE = regexp.MustCompile(`bot\d{6,}(?::|%3[Aa]|%253[Aa])[A-Za-z0-9_-]{20,}`)

// Scrub returns s with the Telegram bot segment and every non-empty secret
// (raw and URL-query-escaped form) replaced.
func Scrub(s string, secrets ...string) string {
	s = botSegmentRE.ReplaceAllString(s, botSegmentPlaceholder)
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, Placeholder)
		// Raw, query-escaped, and escaped twice (a URL inside a query value).
		esc := url.QueryEscape(sec)
		for _, form := range []string{esc, url.QueryEscape(esc)} {
			if form != sec {
				s = strings.ReplaceAll(s, form, Placeholder)
			}
		}
	}
	return s
}

// Error returns err with the same scrubbing applied. A *url.Error keeps its
// type (so Timeout(), Unwrap and errors.As still work); only its URL and, if
// needed, the wrapped Err are rewritten. Any other error whose message holds a
// secret is replaced by a plain message-only error: the chain is cut on
// purpose, so errors.As cannot hand the original URL back to a caller. The
// input is never mutated: errors may be shared.
func Error(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	if ue, ok := err.(*url.Error); ok { //nolint:errorlint // top level only: a wrapped one takes the message path below, which keeps the outer text
		c := *ue
		c.URL = Scrub(ue.URL, secrets...)
		c.Err = Error(ue.Err, secrets...)
		return &c
	}
	msg := err.Error()
	if clean := Scrub(msg, secrets...); clean != msg {
		return &scrubbedError{msg: clean}
	}
	return err
}

type scrubbedError struct{ msg string }

func (e *scrubbedError) Error() string { return e.msg }

// ErrorAttr is a slog attribute named "error" holding err's scrubbed text, for
// log sites that receive errors from code that did not go through
// [HTTPClient] (a file download on a plain http.Client, say). It returns the
// zero Attr, which slog omits, for a nil err. The value is a string, so a
// handler cannot reach the original error.
func ErrorAttr(err error, secrets ...string) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.String("error", Scrub(err.Error(), secrets...))
}

// HTTPClient wraps an *http.Client so every error from Do is scrubbed. It
// satisfies the HTTPClient interface of the Telegram SDKs.
type HTTPClient struct {
	inner   *http.Client
	secrets []string
}

// NewHTTPClient wraps c; nil means &http.Client{}. The Telegram bot segment is
// always scrubbed; secrets adds literal values (the token itself, so that a
// token of unusual shape is covered too).
func NewHTTPClient(c *http.Client, secrets ...string) *HTTPClient {
	if c == nil {
		c = &http.Client{}
	}
	return &HTTPClient{inner: c, secrets: secrets}
}

// Client returns the wrapped *http.Client.
func (h *HTTPClient) Client() *http.Client { return h.inner }

// Do performs the request and scrubs any returned error.
func (h *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := h.inner.Do(req)
	if err != nil {
		return resp, Error(err, h.secrets...)
	}
	return resp, nil
}

// Logger adapts a *slog.Logger to the SDKs' BotLogger interface
// (Println/Printf) and scrubs everything it prints. Install it with the SDK's
// SetLogger so GetUpdatesChan's failed-poll lines cannot carry the token.
// It is safe for concurrent use.
type Logger struct {
	l       *slog.Logger
	mu      sync.RWMutex
	secrets []string
}

// NewLogger returns a Logger writing at Warn level to l (nil means
// slog.Default()). The SDK prints failed polls and, when a bot has Debug set,
// its debug traces (request endpoints and responses, which include message
// text) through it; with Debug=true those traces are also logged at Warn.
func NewLogger(l *slog.Logger, secrets ...string) *Logger {
	if l == nil {
		l = slog.Default()
	}
	return &Logger{l: l, secrets: append([]string(nil), secrets...)}
}

// AddSecrets adds literal secrets to scrub from now on (a second bot's token,
// say). Safe to call while the logger is in use.
func (lg *Logger) AddSecrets(secrets ...string) {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.secrets = append(lg.secrets, secrets...)
}

func (lg *Logger) scrub(s string) string {
	lg.mu.RLock()
	defer lg.mu.RUnlock()
	return Scrub(strings.TrimRight(s, "\n"), lg.secrets...)
}

// Println implements the SDK's BotLogger.
func (lg *Logger) Println(v ...any) {
	lg.l.Warn(lg.scrub(fmt.Sprintln(v...)), "component", "tgbotapi")
}

// Printf implements the SDK's BotLogger.
func (lg *Logger) Printf(format string, v ...any) {
	lg.l.Warn(lg.scrub(fmt.Sprintf(format, v...)), "component", "tgbotapi")
}
