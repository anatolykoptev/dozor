package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// satoriEndpoint is the satori-render sidecar URL. Override via env for tests.
func satoriEndpoint() string {
	if v := os.Getenv("DOZOR_SATORI_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:8910/render"
}

// RenderAlertCard turns an alert into a Telegram-ready PNG via the satori-render sidecar.
//
// Pure template — NO LLM in this path. Severity drives palette + emoji; service name
// is shown as the brand line; title and description are typeset; suggested action is
// rendered as a visual call-to-action band when present.
//
// Returns nil bytes + error when the sidecar is unreachable or fails. Callers should
// fall back to plain-text notifications in that case.
func RenderAlertCard(ctx context.Context, a Alert) ([]byte, error) {
	body := buildAlertCardHTML(a)
	payload, err := json.Marshal(map[string]any{
		"html":   body,
		"width":  1200,
		"height": 630,
	})
	if err != nil {
		return nil, fmt.Errorf("alert_card: marshal: %w", err)
	}

	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodPost, satoriEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("alert_card: new request: %w", err)
	}
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alert_card: satori unreachable: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("alert_card: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("alert_card: satori HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	if len(bodyBytes) < 8 || string(bodyBytes[:8]) != "\x89PNG\r\n\x1a\n" {
		return nil, fmt.Errorf("alert_card: response is not PNG (len=%d)", len(bodyBytes))
	}
	return bodyBytes, nil
}

// alertPalette maps severity levels to colors. Backgrounds use raw hex via inline
// style="background:..." because Satori's default Tailwind config does not know
// the *-950 dark shades. Foreground colors use Tailwind classes which work reliably.
// Palettes were eyeballed against real Telegram dark-mode + light-mode previews.
type alertPalette struct {
	BG     string // hex background (inline style)
	Accent string // Tailwind accent text color (severity badge)
	Title  string // Tailwind title color
	Body   string // Tailwind description color
	Footer string // Tailwind footer color
	Emoji  string // Inline emoji prefix
	Label  string // severity label, e.g. "CRITICAL"
}

func paletteFor(level AlertLevel) alertPalette {
	switch level {
	case AlertCritical:
		return alertPalette{
			BG: "#450a0a", Accent: "text-red-400", Title: "text-white",
			Body: "text-red-100", Footer: "text-red-300",
			Emoji: "🔥", Label: "CRITICAL",
		}
	case AlertError:
		return alertPalette{
			BG: "#431407", Accent: "text-orange-400", Title: "text-white",
			Body: "text-orange-100", Footer: "text-orange-300",
			Emoji: "🚨", Label: "ERROR",
		}
	case AlertWarning:
		return alertPalette{
			BG: "#451a03", Accent: "text-amber-400", Title: "text-white",
			Body: "text-amber-100", Footer: "text-amber-300",
			Emoji: "⚠️", Label: "WARNING",
		}
	default:
		return alertPalette{
			BG: "#0f172a", Accent: "text-slate-300", Title: "text-white",
			Body: "text-slate-200", Footer: "text-slate-400",
			Emoji: "ℹ️", Label: "INFO",
		}
	}
}

// buildAlertCardHTML composes a Tailwind+Satori-conformant card. Every multi-child
// container declares display:flex via tw="flex ..." classes — required by Satori.
func buildAlertCardHTML(a Alert) string {
	p := paletteFor(a.Level)
	title := truncateAlertField(html.EscapeString(a.Title), 90)
	description := truncateAlertField(html.EscapeString(a.Description), 200)
	service := html.EscapeString(a.Service)
	if service == "" {
		service = "dozor"
	}
	suggestion := truncateAlertField(html.EscapeString(a.SuggestedAction), 120)
	timestamp := a.Timestamp.Format("2006-01-02 15:04:05 MST")
	if a.Timestamp.IsZero() {
		timestamp = time.Now().Format("2006-01-02 15:04:05 MST")
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<div tw="flex flex-col w-full h-full p-16 justify-between" style="background:%s">`, p.BG)

	// Header: severity badge + service name
	fmt.Fprintf(&b, `<div tw="flex items-center justify-between">`)
	fmt.Fprintf(&b, `<div tw="flex items-center"><div tw="flex %s text-3xl font-black uppercase tracking-widest">%s %s</div></div>`, p.Accent, p.Emoji, p.Label)
	fmt.Fprintf(&b, `<div tw="flex %s text-2xl font-bold">%s</div>`, p.Footer, service)
	fmt.Fprintf(&b, `</div>`)

	// Title block
	fmt.Fprintf(&b, `<div tw="flex flex-col">`)
	fmt.Fprintf(&b, `<div tw="flex %s text-6xl font-black leading-tight tracking-tight">%s</div>`, p.Title, title)
	if description != "" {
		fmt.Fprintf(&b, `<div tw="flex %s text-2xl mt-6 leading-relaxed">%s</div>`, p.Body, description)
	}
	fmt.Fprintf(&b, `</div>`)

	// Footer: suggested action (if any) + timestamp
	fmt.Fprintf(&b, `<div tw="flex flex-col">`)
	if suggestion != "" {
		fmt.Fprintf(&b, `<div tw="flex %s text-xl font-bold mb-3">→ %s</div>`, p.Accent, suggestion)
	}
	fmt.Fprintf(&b, `<div tw="flex %s text-lg">%s</div>`, p.Footer, timestamp)
	fmt.Fprintf(&b, `</div>`)

	fmt.Fprintf(&b, `</div>`)
	return b.String()
}

func truncateAlertField(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
