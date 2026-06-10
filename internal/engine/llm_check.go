package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/provider"
	kitllm "github.com/anatolykoptev/go-kit/llm"
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

	// A) Direct Gemini API key validation via Google REST API (cheap — no tokens consumed).
	// Uses raw HTTP GET to ?key=... endpoint; different protocol from OpenAI-compat.
	for _, key := range cfg.GeminiAPIKeys {
		if a := checkGeminiKey(ctx, client, key); a != nil {
			alerts = append(alerts, *a)
		}
	}

	// B) Proxy chain test (1 token per probed model via CLIProxyAPI).
	// DOZOR_LLM_CHECK_MODELS mirrors the production fallback chain: go-kit llm
	// clients (WithModelFallback) survive any single model failing — 413/429/5xx
	// advance the chain. The canary probes the SAME way: models in order, first
	// success = healthy. Only a fully dead chain — the condition production
	// actually feels — raises an alert.
	if cfg.LLMCheckURL != "" && cfg.LLMCheckAPIKey != "" && len(cfg.LLMCheckModels) > 0 {
		if a := checkProxyChain(ctx, cfg.LLMCheckURL, cfg.LLMCheckAPIKey, cfg.LLMCheckModels); a != nil {
			alerts = append(alerts, *a)
		}
	}

	return alerts
}

// googleAPIError represents the error envelope returned by Google API on non-200 responses.
type googleAPIError struct {
	Error struct {
		Code    int    `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// googleAPIErrorMaxLen is the max rune count of the error message in alert descriptions.
const googleAPIErrorMaxLen = 120

// googleAPIBodyMaxBytes caps body read on a Gemini API response. 4 KiB is
// well above any legitimate JSON envelope and bounds memory if upstream
// returns an oversized or streaming body (hostile or misconfigured proxy).
const googleAPIBodyMaxBytes = 4096

// parseGoogleAPIError reads the response body and attempts to parse the Google API error envelope.
// Returns status string (e.g. "UNAVAILABLE") and message (truncated to googleAPIErrorMaxLen),
// or empty strings if the body cannot be parsed.
func parseGoogleAPIError(body []byte) (status, message string) {
	var env googleAPIError
	if err := json.Unmarshal(body, &env); err != nil {
		return "", ""
	}
	status = env.Error.Status
	message = truncateUTF8(env.Error.Message, googleAPIErrorMaxLen)
	return status, message
}

// truncateUTF8 truncates s to at most maxRunes runes (not bytes), preserving
// UTF-8 boundaries. Multi-byte characters (Cyrillic, CJK, emoji) are never
// split mid-codepoint.
func truncateUTF8(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// checkGeminiKey validates a single Gemini API key by listing models.
func checkGeminiKey(ctx context.Context, client *http.Client, key string) *Alert {
	url := geminiModelsURL + "?key=" + key
	masked := maskKey(key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         llmServicePrefix + "gemini-key-" + masked,
			Title:           "request error",
			Description:     err.Error(),
			SuggestedAction: "Check key format",
			Timestamp:       time.Now(),
		}
	}

	resp, err := client.Do(req) //nolint:gosec // request directly to Gemini API
	if err != nil {
		return &Alert{
			Level:           AlertError,
			Service:         llmServicePrefix + "gemini-key-" + masked,
			Title:           "unreachable",
			Description:     err.Error(),
			SuggestedAction: "Check network connectivity to Google API",
			Timestamp:       time.Now(),
		}
	}
	defer resp.Body.Close()

	// Read body for all responses — Google API provides a JSON error envelope on non-200.
	// Cap at 4 KiB to bound memory if upstream returns oversized/streaming body.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, googleAPIBodyMaxBytes)) //nolint:errcheck // best-effort; empty body is handled below

	switch resp.StatusCode {
	case http.StatusOK:
		slog.Debug("gemini key ok", slog.String("key", masked))
		return nil

	case http.StatusTooManyRequests:
		return &Alert{
			Level:           AlertWarning,
			Service:         llmServicePrefix + "gemini-key-" + masked,
			Title:           "quota exceeded",
			Description:     buildGoogleAPIDesc(body, resp.StatusCode),
			SuggestedAction: "Wait for quota reset or rotate key",
			Timestamp:       time.Now(),
		}

	case http.StatusUnauthorized, http.StatusForbidden:
		desc := buildGoogleAPIDesc(body, resp.StatusCode)
		return &Alert{
			Level:           AlertError,
			Service:         llmServicePrefix + "gemini-key-" + masked,
			Title:           fmt.Sprintf("invalid key (HTTP %d)", resp.StatusCode),
			Description:     desc,
			SuggestedAction: "Rotate or remove this API key",
			Timestamp:       time.Now(),
		}

	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		desc := buildGoogleAPIDesc(body, resp.StatusCode)
		apiStatus, _ := parseGoogleAPIError(body)
		titleTag := apiStatus
		if titleTag == "" {
			titleTag = "error"
		}
		return &Alert{
			Level:           AlertWarning,
			Service:         llmServicePrefix + "gemini-key-" + masked,
			Title:           fmt.Sprintf("Google API upstream %s (HTTP %d)", titleTag, resp.StatusCode),
			Description:     desc,
			SuggestedAction: "Transient outage — retry; if persistent >1h check Google status page",
			Timestamp:       time.Now(),
		}

	default:
		desc := buildGoogleAPIDesc(body, resp.StatusCode)
		return &Alert{
			Level:           AlertError,
			Service:         llmServicePrefix + "gemini-key-" + masked,
			Title:           fmt.Sprintf("unexpected HTTP %d", resp.StatusCode),
			Description:     desc,
			SuggestedAction: "Investigate; status code is unusual for this endpoint",
			Timestamp:       time.Now(),
		}
	}
}

// buildGoogleAPIDesc builds an alert description from the raw Google API response body.
// If the body contains a parseable Google API error envelope, returns "STATUS: message".
// Otherwise falls back to "HTTP <code>".
func buildGoogleAPIDesc(body []byte, statusCode int) string {
	apiStatus, apiMsg := parseGoogleAPIError(body)
	if apiStatus != "" {
		if apiMsg != "" {
			return apiStatus + ": " + apiMsg
		}
		return apiStatus
	}
	return fmt.Sprintf("HTTP %d", statusCode)
}

// checkProxyChain probes the check models as a fallback chain. The first
// healthy model proves the production path is alive (degraded-but-alive is
// logged, not alerted). A fully dead chain returns ONE aggregate alert with a
// STABLE service id ("llm:check-chain") so repeated outages dedup correctly.
func checkProxyChain(ctx context.Context, baseURL, apiKey string, models []string) *Alert {
	var failures []string
	for i, model := range models {
		a := checkProxyModel(ctx, baseURL, apiKey, model)
		if a == nil {
			if i > 0 {
				slog.Info("llm check: chain degraded but alive",
					slog.String("healthy_model", model),
					slog.Int("dead_ahead", i),
					slog.String("failures", strings.Join(failures, "; ")))
			}
			return nil
		}
		failures = append(failures,
			strings.TrimPrefix(a.Service, llmServicePrefix)+": "+a.Title)
	}
	return &Alert{
		Level:           AlertError,
		Service:         llmServicePrefix + "check-chain",
		Title:           fmt.Sprintf("all %d check models failed", len(models)),
		Description:     strings.Join(failures, "; "),
		SuggestedAction: "Production fallback chain is exhausted — check CLIProxyAPI and provider quotas",
		Timestamp:       time.Now(),
	}
}

// checkProxyModel tests a single model through the LLM proxy using kitllm.Client.Chat.
// Replaces the previous hand-rolled HTTP POST with chatRequest/chatResponse types.
// Uses max_tokens=1 to minimise cost; any successful response means the model is reachable.
// Error classification uses provider.IsAuth/IsRateLimit/IsServerError over kitllm.APIError.
func checkProxyModel(ctx context.Context, baseURL, apiKey, model string) *Alert {
	kitClient := kitllm.NewClient(baseURL, apiKey, model,
		kitllm.WithMaxRetries(1), // single attempt — health check, not production call
	)

	msgs := []kitllm.Message{{Role: "user", Content: "."}}
	_, err := kitClient.Chat(ctx, msgs, kitllm.WithChatMaxTokens(llmCheckMaxTokens))
	if err == nil {
		slog.Debug("proxy model ok", slog.String("model", model))
		return nil
	}

	// Classify via kitllm.APIError using provider free-funcs.
	var ae *kitllm.APIError
	if errors.As(err, &ae) {
		if provider.IsAuth(err) {
			return &Alert{
				Level:           AlertError,
				Service:         llmServicePrefix + model,
				Title:           fmt.Sprintf("auth failure (HTTP %d)", ae.StatusCode),
				Description:     ae.Body,
				SuggestedAction: "Check DOZOR_LLM_CHECK_API_KEY / DOZOR_LLM_API_KEY credentials",
				Timestamp:       time.Now(),
			}
		}
		if provider.IsRateLimit(err) {
			return &Alert{
				Level:           AlertWarning,
				Service:         llmServicePrefix + model,
				Title:           fmt.Sprintf("rate limited (HTTP %d)", ae.StatusCode),
				Description:     ae.Body,
				SuggestedAction: "Wait for quota reset or reduce probe frequency",
				Timestamp:       time.Now(),
			}
		}
		if provider.IsServerError(err) {
			return &Alert{
				Level:           AlertWarning,
				Service:         llmServicePrefix + model,
				Title:           fmt.Sprintf("upstream error (HTTP %d)", ae.StatusCode),
				Description:     ae.Body,
				SuggestedAction: "Transient outage — retry; if persistent check CLIProxyAPI logs",
				Timestamp:       time.Now(),
			}
		}
		// Other API error (e.g. 400 bad request, 404 model not found).
		return &Alert{
			Level:           AlertError,
			Service:         llmServicePrefix + model,
			Title:           fmt.Sprintf("error (HTTP %d)", ae.StatusCode),
			Description:     ae.Body,
			SuggestedAction: "Check model availability and proxy configuration",
			Timestamp:       time.Now(),
		}
	}

	// Non-API error: network/transport failure.
	return &Alert{
		Level:           AlertError,
		Service:         llmServicePrefix + model,
		Title:           "unreachable",
		Description:     err.Error(),
		SuggestedAction: "Check CLIProxyAPI is running and DOZOR_LLM_CHECK_URL is correct",
		Timestamp:       time.Now(),
	}
}

// FormatLLMAlerts formats LLM health-check alerts as canonical issue lines so
// they are first-class to ExtractIssues (and therefore to dedup, severity
// ranking, and remediation routing) when concatenated into the watch report.
// Each alert is rendered via AlertIssueLine, which gives it a stable per-model
// service name. Previously this emitted "- [LEVEL] title: desc", a shape
// ExtractIssues could not parse — the failures were invisible to the watch
// pipeline and all collapsed to a single dedup hash.
func FormatLLMAlerts(alerts []Alert) string {
	if len(alerts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range alerts {
		b.WriteString(AlertIssueLine(a))
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
