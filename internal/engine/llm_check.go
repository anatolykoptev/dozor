package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// geminiModelsURL is the endpoint for listing Gemini models (validates API key).
	geminiModelsURL = "https://generativelanguage.googleapis.com/v1beta/models"
	// llmCheckTimeout is the HTTP timeout for LLM health checks.
	llmCheckTimeout = 10 * time.Second
	// llmCheckMaxTokens is the max tokens for proxy channel tests (minimal cost).
	llmCheckMaxTokens = 1
	// maskKeySuffixLen is how many chars to show at the start of masked keys.
	maskKeySuffixLen = 11
)

// CheckLLMKeys validates Gemini API keys directly and tests proxy channels.
// Returns alerts for any failures.
func CheckLLMKeys(ctx context.Context, cfg Config) []Alert {
	var alerts []Alert

	client := newHTTPClient(llmCheckTimeout)

	// A) Direct Gemini API key validation (cheap — no tokens consumed).
	for _, key := range cfg.GeminiAPIKeys {
		if a := checkGeminiKey(ctx, client, key); a != nil {
			alerts = append(alerts, *a)
		}
	}

	// B) Proxy channel tests (1 token each via CLIProxyAPI).
	if cfg.LLMCheckURL != "" && cfg.LLMCheckAPIKey != "" {
		for _, model := range cfg.LLMCheckModels {
			if a := checkProxyModel(ctx, client, cfg.LLMCheckURL, cfg.LLMCheckAPIKey, model); a != nil {
				alerts = append(alerts, *a)
			}
		}
	}

	return alerts
}

// checkGeminiKey validates a single Gemini API key by listing models.
func checkGeminiKey(ctx context.Context, client *http.Client, key string) *Alert {
	url := geminiModelsURL + "?key=" + key
	masked := maskKey(key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("Gemini key %s: request error", masked),
			Description:     err.Error(),
			SuggestedAction: "Check key format",
			Timestamp:       time.Now(),
		}
	}

	resp, err := client.Do(req) //nolint:gosec // request directly to Gemini API
	if err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("Gemini key %s: unreachable", masked),
			Description:     err.Error(),
			SuggestedAction: "Check network connectivity to Google API",
			Timestamp:       time.Now(),
		}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // drain body

	switch resp.StatusCode {
	case http.StatusOK:
		slog.Debug("gemini key ok", slog.String("key", masked))
		return nil
	case http.StatusTooManyRequests:
		return &Alert{
			Level:           AlertWarning,
			Service:         "llm",
			Title:           fmt.Sprintf("Gemini key %s: quota exceeded", masked),
			Description:     fmt.Sprintf("HTTP %d — rate limited", resp.StatusCode),
			SuggestedAction: "Wait for quota reset or rotate key",
			Timestamp:       time.Now(),
		}
	default:
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("Gemini key %s: invalid", masked),
			Description:     fmt.Sprintf("HTTP %d", resp.StatusCode),
			SuggestedAction: "Rotate or remove this API key",
			Timestamp:       time.Now(),
		}
	}
}

// chatRequest is the minimal request body for proxy channel tests.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	MaxToks  int           `json:"max_tokens"`
}

// chatMessage is a single message in a chat request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the minimal response from proxy channel tests.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// checkProxyModel tests a single model through the LLM proxy.
func checkProxyModel(ctx context.Context, client *http.Client, baseURL, apiKey, model string) *Alert {
	body, _ := json.Marshal(chatRequest{
		Model:    model,
		Messages: []chatMessage{{Role: "user", Content: "ok"}},
		MaxToks:  llmCheckMaxTokens,
	})

	url := baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("LLM proxy %s: request error", model),
			Description:     err.Error(),
			SuggestedAction: "Check proxy configuration",
			Timestamp:       time.Now(),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req) //nolint:gosec // proxied request
	if err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("LLM proxy %s: unreachable", model),
			Description:     err.Error(),
			SuggestedAction: "Check CLIProxyAPI is running",
			Timestamp:       time.Now(),
		}
	}
	defer resp.Body.Close()

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("LLM proxy %s: bad response", model),
			Description:     err.Error(),
			SuggestedAction: "Check proxy response format",
			Timestamp:       time.Now(),
		}
	}

	if cr.Error != nil {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("LLM proxy %s: error", model),
			Description:     cr.Error.Message,
			SuggestedAction: "Check model availability and credentials",
			Timestamp:       time.Now(),
		}
	}

	if len(cr.Choices) == 0 || cr.Choices[0].Message.Content == "" {
		return &Alert{
			Level:           AlertError,
			Service:         "llm",
			Title:           fmt.Sprintf("LLM proxy %s: empty response", model),
			Description:     "No choices returned",
			SuggestedAction: "Check model availability",
			Timestamp:       time.Now(),
		}
	}

	slog.Debug("proxy model ok", slog.String("model", model))
	return nil
}

// FormatLLMAlerts formats LLM health check alerts for text display.
func FormatLLMAlerts(alerts []Alert) string {
	if len(alerts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("LLM Health Issues:\n")
	for _, a := range alerts {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", a.Level, a.Title, a.Description)
	}
	return b.String()
}

// maskKey masks an API key for display: "AIzaSyCcPfX..." from "AIzaSyCcPfXpZMuN5...".
func maskKey(key string) string {
	if len(key) <= maskKeySuffixLen {
		return key
	}
	return key[:maskKeySuffixLen] + "..."
}
