package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// ddgExtraMatches is extra regex matches fetched beyond count for DDG to allow filtering.
	ddgExtraMatches = 5
	// webFetchErrorThreshold is the HTTP status code threshold for treating a response as an error.
	webFetchErrorThreshold = 400
	// webFetchContentMaxLen is the maximum content length returned by FetchURL.
	webFetchContentMaxLen = 40000
)

// WebSearchConfig holds search provider configuration.
type WebSearchConfig struct {
	BraveAPIKey          string
	BraveMaxResults      int
	BraveEnabled         bool
	DuckDuckGoMaxResults int
	DuckDuckGoEnabled    bool
	PerplexityAPIKey     string
	PerplexityMaxResults int
	PerplexityEnabled    bool
}

// SearchProvider interface for different search backends.
type SearchProvider interface {
	Search(ctx context.Context, query string, count int) (string, error)
	Name() string
}

// BraveSearchProvider uses Brave Search API.
type BraveSearchProvider struct {
	apiKey string
}

func (p *BraveSearchProvider) Name() string { return "brave" }

func (p *BraveSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // brave API
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var searchResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Web.Results
	if len(results) == 0 {
		return "No results for: " + query, nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via Brave)", query))
	for i, item := range results {
		if i >= count {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Description != "" {
			lines = append(lines, "   " + item.Description)
		}
	}

	return strings.Join(lines, "\n"), nil
}

// DuckDuckGoSearchProvider uses DuckDuckGo HTML search (no API key required).
type DuckDuckGoSearchProvider struct{}

func (p *DuckDuckGoSearchProvider) Name() string { return "duckduckgo" }

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func (p *DuckDuckGoSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // DuckDuckGo API
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return p.extractResults(string(body), count, query)
}

func (p *DuckDuckGoSearchProvider) extractResults(html string, count int, query string) (string, error) {
	// Extract result links
	reLink := regexp.MustCompile(`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>([^<]+)</a>`)
	matches := reLink.FindAllStringSubmatch(html, count+ddgExtraMatches)

	if len(matches) == 0 {
		return "No results found for: " + query, nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via DuckDuckGo)", query))

	// Extract snippets
	reSnippet := regexp.MustCompile(`<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>([^<]+)</a>`)
	snippetMatches := reSnippet.FindAllStringSubmatch(html, count+ddgExtraMatches)

	maxItems := min(len(matches), count)
	for i := 0; i < maxItems; i++ {
		urlStr := matches[i][1]
		title := stripTags(matches[i][2])
		title = strings.TrimSpace(title)

		// URL decoding for DDG redirect
		if strings.Contains(urlStr, "uddg=") {
			if u, err := url.QueryUnescape(urlStr); err == nil {
				idx := strings.Index(u, "uddg=")
				if idx != -1 {
					urlStr = u[idx+5:]
				}
			}
		}

		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, urlStr))

		// Attach snippet if available
		if i < len(snippetMatches) {
			snippet := strings.TrimSpace(snippetMatches[i][1])
			if snippet != "" {
				lines = append(lines, "   "+snippet)
			}
		}
	}

	return strings.Join(lines, "\n"), nil
}

// PerplexitySearchProvider uses Perplexity API.
type PerplexitySearchProvider struct {
	apiKey string
}

func (p *PerplexitySearchProvider) Name() string { return "perplexity" }

func (p *PerplexitySearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := "https://api.perplexity.ai/chat/completions"

	payload := map[string]interface{}{
		"model": "sonar",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a search assistant. Provide concise search results with titles, URLs, and brief descriptions. Format:\n1. Title\n   URL\n   Description\n\nDo not add extra commentary."},
			{"role": "user", "content": fmt.Sprintf("Search for: %s. Provide up to %d relevant results.", query, count)},
		},
		"max_tokens": 1000,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // Perplexity API
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var perplexityResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &perplexityResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(perplexityResp.Choices) == 0 {
		return "No results for: " + query, nil
	}

	return fmt.Sprintf("Results for: %s (via Perplexity)\n\n%s", query, perplexityResp.Choices[0].Message.Content), nil
}

// WebSearch performs a web search using configured providers.
func WebSearch(ctx context.Context, cfg WebSearchConfig, query string, count int) (string, error) {
	if count <= 0 {
		count = 5
	}

	var providers []SearchProvider

	// Add providers in priority order
	if cfg.BraveEnabled && cfg.BraveAPIKey != "" {
		providers = append(providers, &BraveSearchProvider{apiKey: cfg.BraveAPIKey})
	}
	if cfg.PerplexityEnabled && cfg.PerplexityAPIKey != "" {
		providers = append(providers, &PerplexitySearchProvider{apiKey: cfg.PerplexityAPIKey})
	}
	if cfg.DuckDuckGoEnabled {
		providers = append(providers, &DuckDuckGoSearchProvider{})
	}

	if len(providers) == 0 {
		// Default to DuckDuckGo if no providers configured
		providers = append(providers, &DuckDuckGoSearchProvider{})
	}

	var lastErr error
	for _, p := range providers {
		result, err := p.Search(ctx, query, count)
		if err != nil {
			lastErr = err
			continue
		}
		return result, nil
	}

	if lastErr != nil {
		return "", fmt.Errorf("all search providers failed: %w", lastErr)
	}
	return "", errors.New("no search providers available")
}

// WebFetch fetches content from a URL.
func WebFetch(ctx context.Context, fetchURL string, maxLength int) (string, error) {
	if maxLength <= 0 {
		maxLength = 50000
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,fetchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Do(req) //nolint:gosec // requested URL to fetch content
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= webFetchErrorThreshold {
		return "", fmt.Errorf("HTTP error: %d %s", resp.StatusCode, resp.Status)
	}

	// Read with limit
	limitedReader := io.LimitReader(resp.Body, int64(maxLength+1))
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	truncated := len(body) > maxLength
	if truncated {
		body = body[:maxLength]
	}

	// Extract text content from HTML
	content := extractTextContent(string(body))

	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n", fetchURL)
	fmt.Fprintf(&sb, "Title: %s\n", extractTitle(string(body)))
	if truncated {
		fmt.Fprintf(&sb, "Content (truncated to %d chars):\n\n", maxLength)
	} else {
		sb.WriteString("Content:\n\n")
	}
	sb.WriteString(content)

	return sb.String(), nil
}

// stripTags removes HTML tags from content.
func stripTags(content string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return re.ReplaceAllString(content, "")
}

// extractTitle extracts the title from HTML.
func extractTitle(html string) string {
	re := regexp.MustCompile(`<title[^>]*>([^<]+)</title>`)
	matches := re.FindStringSubmatch(html)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// extractTextContent extracts readable text from HTML.
func extractTextContent(html string) string {
	// Remove script, style, and noscript blocks (Go regexp has no backreferences).
	html = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`).ReplaceAllString(html, "")

	// Remove HTML comments
	re := regexp.MustCompile(`(?s)<!--.*?-->`)
	html = re.ReplaceAllString(html, "")

	// Replace block elements with newlines
	re = regexp.MustCompile(`(?i)<(br|p|div|section|article|header|footer|nav|main|aside|h[1-6]|li|tr|td)[^>]*>`)
	html = re.ReplaceAllString(html, "\n")

	// Remove remaining tags
	html = stripTags(html)

	// Decode HTML entities
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")

	// Clean up whitespace
	lines := strings.Split(html, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}

	// Limit output length
	content := strings.Join(result, "\n")
	if len(content) > webFetchContentMaxLen {
		content = content[:webFetchContentMaxLen] + "\n... (content truncated)"
	}

	return content
}
