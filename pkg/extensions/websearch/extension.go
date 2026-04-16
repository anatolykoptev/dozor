package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/pkg/extensions"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WebSearchExtension provides web search and fetch capabilities.
type WebSearchExtension struct{}

func New() *WebSearchExtension {
	return &WebSearchExtension{}
}

func (e *WebSearchExtension) Name() string { return "websearch" }

func (e *WebSearchExtension) GetCapabilities() extensions.Capabilities {
	return extensions.Capabilities{
		Tools:     false,
		MCPTools:  true,
		Config:    true,
		Lifecycle: false,
	}
}

func (e *WebSearchExtension) ValidateConfig(config *engine.Config) extensions.ConfigValidation {
	var errs []extensions.ConfigError

	if !config.DuckDuckGoEnabled && !config.BraveEnabled && !config.PerplexityEnabled {
		errs = append(errs, extensions.ConfigError{
			Field:   "DOZOR_DDG_ENABLED / DOZOR_BRAVE_ENABLED / DOZOR_PERPLEXITY_ENABLED",
			Message: "at least one search provider must be enabled",
		})
	}
	if config.BraveEnabled && config.BraveAPIKey == "" {
		errs = append(errs, extensions.ConfigError{
			Field:   "DOZOR_BRAVE_API_KEY",
			Message: "required when Brave search is enabled",
		})
	}
	if config.PerplexityEnabled && config.PerplexityAPIKey == "" {
		errs = append(errs, extensions.ConfigError{
			Field:   "DOZOR_PERPLEXITY_API_KEY",
			Message: "required when Perplexity is enabled",
		})
	}

	return extensions.ConfigValidation{OK: len(errs) == 0, Errors: errs}
}

func (e *WebSearchExtension) GetConfigHints() map[string]extensions.ConfigHint {
	return map[string]extensions.ConfigHint{
		"DOZOR_BRAVE_API_KEY": {
			Label:     "Brave Search API Key",
			Help:      "Get from https://brave.com/search/api/",
			Sensitive: true,
			Required:  false,
		},
		"DOZOR_DDG_ENABLED": {
			Label: "Enable DuckDuckGo",
			Help:  "Free search provider, no API key required",
		},
		"DOZOR_PERPLEXITY_API_KEY": {
			Label:     "Perplexity API Key",
			Help:      "Get from https://www.perplexity.ai/settings/api",
			Sensitive: true,
			Required:  false,
		},
	}
}

func (e *WebSearchExtension) Register(ctx context.Context, extCtx *extensions.Context) error {
	config := extCtx.Config
	log := extCtx.Runtime.Logger

	enabled := []string{}
	if config.DuckDuckGoEnabled {
		enabled = append(enabled, "duckduckgo")
	}
	if config.BraveEnabled {
		enabled = append(enabled, "brave")
	}
	if config.PerplexityEnabled {
		enabled = append(enabled, "perplexity")
	}
	log.Info("registering web tools", "providers", enabled)

	// Register server_web_search
	mcp.AddTool(extCtx.MCPServer, &mcp.Tool{
		Name:        "server_web_search",
		Description: "Search the web for information. Supports Brave, DuckDuckGo, and Perplexity providers. Use to find solutions, documentation, or security advisories.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input struct {
		Query string `json:"query"`
		Count int    `json:"count,omitempty"`
	}) (*mcp.CallToolResult, engine.TextOutput, error) {
		count := input.Count
		if count <= 0 {
			count = 5
		}

		searchCfg := engine.WebSearchConfig{
			BraveAPIKey:          config.BraveAPIKey,
			BraveMaxResults:      config.BraveMaxResults,
			BraveEnabled:         config.BraveEnabled,
			DuckDuckGoMaxResults: config.DuckDuckGoMaxResults,
			DuckDuckGoEnabled:    config.DuckDuckGoEnabled,
			PerplexityAPIKey:     config.PerplexityAPIKey,
			PerplexityMaxResults: config.PerplexityMaxResults,
			PerplexityEnabled:    config.PerplexityEnabled,
		}

		result, err := engine.WebSearch(ctx, searchCfg, input.Query, count)
		if err != nil {
			return nil, engine.TextOutput{}, err
		}
		return nil, engine.TextOutput{Text: result}, nil
	})

	// Register server_web_fetch (delegates to ox-browser for better content extraction)
	oxBrowserURL := os.Getenv("OX_BROWSER_URL")
	if oxBrowserURL == "" {
		oxBrowserURL = "http://localhost:8901"
	}

	mcp.AddTool(extCtx.MCPServer, &mcp.Tool{
		Name:        "server_web_fetch",
		Description: "Fetch and extract text content from a URL. Use to read web pages, documentation, or API responses.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input struct {
		URL       string `json:"url"`
		MaxLength int    `json:"max_length,omitempty"`
	}) (*mcp.CallToolResult, engine.TextOutput, error) {
		maxLength := input.MaxLength
		if maxLength <= 0 {
			maxLength = 50000
		}

		result, err := fetchViaOxBrowser(ctx, oxBrowserURL, input.URL, maxLength)
		if err != nil {
			log.Warn("ox-browser fetch failed, falling back to engine.WebFetch", "error", err)
			result, err = engine.WebFetch(ctx, input.URL, maxLength)
			if err != nil {
				return nil, engine.TextOutput{}, err
			}
		}
		return nil, engine.TextOutput{Text: result}, nil
	})

	return nil
}

type oxBrowserRequest struct {
	URL       string `json:"url"`
	Format    string `json:"format"`
	MaxLength int    `json:"max_length"`
}

type oxBrowserResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url"`
}

func fetchViaOxBrowser(ctx context.Context, baseURL, targetURL string, maxLength int) (string, error) {
	reqBody := oxBrowserRequest{
		URL:       targetURL,
		Format:    "text",
		MaxLength: maxLength,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ox-browser request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/read", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create ox-browser request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ox-browser request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("ox-browser returned HTTP %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read ox-browser response: %w", err)
	}

	var oxResp oxBrowserResponse
	if err := json.Unmarshal(respBody, &oxResp); err != nil {
		return "", fmt.Errorf("failed to parse ox-browser response: %w", err)
	}

	return fmt.Sprintf("URL: %s\nTitle: %s\nContent:\n\n%s", targetURL, oxResp.Title, oxResp.Content), nil
}
