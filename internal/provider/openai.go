package provider

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	"github.com/anatolykoptev/go-kit/tracing"
	"github.com/anatolykoptev/go-kit/tracing/httpmw"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sync"
)


// cacheHitLogOnce gates a single ops-visible log line on the first
// observed prompt-cache hit (cached_tokens > 0 in usage). Confirms
// caching is working without spamming the journal at request rate.
// Per-call cached_tokens still lands on the llm.chat span attributes
// for full visibility in Jaeger.
var cacheHitLogOnce sync.Once
// OpenAI is an OpenAI-compatible HTTP provider.
type OpenAI struct {
	apiURL   string
	apiKey   string
	model    string
	client   *http.Client
	maxIters int
}

// NewOpenAI constructs an OpenAI-compat LLM provider from env. On empty
// DOZOR_LLM_API_KEY, returns (unavailable{}, false) — callers should log
// and degrade. The bool reports whether a real client was built.
//
// Never returns nil. The Provider returned is always usable; on the
// disabled path it returns ErrUnavailable from every Chat call.
func NewOpenAI() (Provider, bool) {
	apiURL := os.Getenv("DOZOR_LLM_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8787/v1"
	}
	model := os.Getenv("DOZOR_LLM_MODEL")
	if model == "" {
		model = "gemini-3.1-flash-lite-preview"
	}
	apiKey := os.Getenv("DOZOR_LLM_API_KEY")
	if apiKey == "" {
		return unavailable{}, false
	}
	maxIters := 10
	if v := os.Getenv("DOZOR_MAX_TOOL_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxIters = n
		}
	}
	o := &OpenAI{
		apiURL:   apiURL,
		apiKey:   apiKey,
		model:    model,
		maxIters: maxIters,
		// 90s — under burst, 300s pinned message-slices for 5 min × N goroutines
		// → 6.3 GB RSS peak (incident 2026-05-12). Monitoring/triage rarely
		// needs >60s; streaming not used.
		// NOTE: fallback chain (internal/provider/fallback.go) wraps each provider
		// with its own client; this timeout applies to every OpenAI instance
		// constructed by NewOpenAI, including the fallback chain's secondary.
		client: &http.Client{Timeout: 90 * time.Second, Transport: httpmw.WrapTransport(&http.Transport{})},
	}
	return o, true
}

// MaxIterations returns the configured max tool call iterations.
func (o *OpenAI) MaxIterations() int { return o.maxIters }

// maxRetries for transient errors (429, 5xx).
const (
	chatMaxRetries   = 3
	chatInitialDelay = 2 * time.Second
	chatMaxDelay     = 30 * time.Second
	// chatJitterDivisor is the divisor for jitter calculation in exponential backoff.
	chatJitterDivisor = 4
)

// Chat sends a chat completion request and returns the response.
// Retries up to chatMaxRetries times on transient errors (429, 5xx).
func (o *OpenAI) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	return o.chatWithRetry(ctx, messages, tools)
}

func (o *OpenAI) chatWithRetry(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	ctx, span := tracing.Start(ctx, "llm.chat",
		attribute.String("llm.model", o.model),
		attribute.String("llm.url", o.apiURL),
		attribute.Int("llm.messages.count", len(messages)),
		attribute.Int("llm.tools.count", len(tools)))
	defer span.End()

	var lastErr error
	for attempt := 0; attempt <= chatMaxRetries; attempt++ {
		resp, err := o.doChatCtx(ctx, messages, tools)
		if err == nil {
			span.SetAttributes(
				attribute.Int("llm.attempts", attempt+1),
				attribute.String("llm.finish_reason", resp.FinishReason),
				attribute.Int("llm.response.length", len(resp.Content)),
				attribute.Int("llm.tool_calls.count", len(resp.ToolCalls)))
			return resp, nil
		}
		lastErr = err

		retry, delay := shouldRetry(err, attempt)
		if !retry {
			break
		}
		if sleepCtx(ctx, delay) != nil {
			return nil, lastErr
		}
	}
	tracing.RecordError(span, lastErr)
	return nil, lastErr
}

// shouldRetry classifies err and returns whether to retry and the delay to wait.
// Returns false when the error is an auth failure, a non-transient provider error,
// or when attempt has reached chatMaxRetries.
func shouldRetry(err error, attempt int) (bool, time.Duration) {
	// Context cancellation / deadline — never retry; the caller's ctx is gone.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		// Network error — retry with backoff.
		if attempt >= chatMaxRetries {
			return false, 0
		}
		delay := chatBackoff(attempt)
		slog.Warn("LLM network error, retrying", slog.Int("attempt", attempt+1), slog.Duration("delay", delay))
		return true, delay
	}
	// Auth errors — fail immediately.
	if pe.IsAuth() {
		return false, 0
	}
	// Transient (429, 5xx) — retry with backoff.
	if pe.IsTransient() && attempt < chatMaxRetries {
		delay := chatBackoff(attempt)
		if pe.IsRateLimit() && pe.RetryAfter > delay && pe.RetryAfter <= chatMaxDelay {
			delay = pe.RetryAfter
		}
		slog.Warn("LLM transient error, retrying",
			slog.Int("status", pe.StatusCode),
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay))
		return true, delay
	}
	return false, 0
}

func chatBackoff(attempt int) time.Duration {
	delay := chatInitialDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	jitter := time.Duration(rand.Int64N(int64(delay / chatJitterDivisor))) //nolint:gosec // non-cryptographic jitter for retry backoff
	delay += jitter
	if delay > chatMaxDelay {
		delay = chatMaxDelay
	}
	return delay
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// doChatCtx delegates HTTP/JSON mechanics to kitllm.Client.Chat. The
// retry/classification policy lives in chatWithRetry (Phase 3 will move
// it into a kitllm.Middleware).
//
// kitllm's internal retry is disabled (WithMaxRetries(1) = single
// attempt) because dozor owns the retry loop with its own backoff,
// jitter, and ProviderError-aware classification.
func (o *OpenAI) doChatCtx(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	client := kitllm.NewClient(o.apiURL, o.apiKey, o.model,
		kitllm.WithHTTPClient(o.client),
		kitllm.WithMaxRetries(1), // dozor owns retry; 1 = single attempt
	)

	// WithMessageTimestamps materialises Message.ChatTime as a bracketed
	// "[YYYY-MM-DD HH:MM UTC] " prefix on user/assistant text so the model
	// can reason about message recency. System messages have empty
	// ChatTime and are not modified — keeps the prompt-cache prefix
	// stable across turns.
	opts := []kitllm.ChatOption{kitllm.WithMessageTimestamps()}
	if len(tools) > 0 {
		opts = append(opts, kitllm.WithTools(toKitTools(tools)))
		opts = append(opts, kitllm.WithToolChoice("auto"))
	}

	resp, err := client.Chat(ctx, toKitMessages(messages), opts...)
	if err != nil {
		// Map kitllm.APIError -> dozor.ProviderError so shouldRetry can
		// classify auth/rate-limit/server semantics. Non-API errors
		// (transport, empty-choices, JSON decode) flow through as-is —
		// shouldRetry treats them as network-class transient errors.
		var apiErr *kitllm.APIError
		if errors.As(err, &apiErr) {
			return nil, parseProviderError(apiErr.StatusCode, []byte(apiErr.Body))
		}
		return nil, err
	}

	// Surface cache-hit token counts on the active llm.chat span so we
	// can verify prompt caching from Jaeger. Zero is normal (cold start
	// or non-cacheable provider) and not worth a span event.
	if resp.Usage != nil && resp.Usage.CachedTokens > 0 {
		if span := trace.SpanFromContext(ctx); span != nil {
			span.SetAttributes(
				attribute.Int("llm.cache.read_tokens", resp.Usage.CachedTokens),
				attribute.Int("llm.cache.creation_tokens", resp.Usage.CacheCreationTokens),
			)
		}
		cacheHitLogOnce.Do(func() {
			slog.Info("LLM prompt cache active",
				slog.Int("cache.read_tokens", resp.Usage.CachedTokens),
				slog.Int("cache.creation_tokens", resp.Usage.CacheCreationTokens),
				slog.Int("prompt.tokens", resp.Usage.PromptTokens),
				slog.String("model", o.model))
		})
	}

	out := fromKitResponse(resp)
	// Pre-parse tool call arguments and populate the legacy Name field
	// for downstream code in internal/agent that reads call.Args.
	for i := range out.ToolCalls {
		if out.ToolCalls[i].Function == nil {
			continue
		}
		out.ToolCalls[i].Name = out.ToolCalls[i].Function.Name
		var args map[string]any
		if err := json.Unmarshal([]byte(out.ToolCalls[i].Function.Arguments), &args); err == nil {
			out.ToolCalls[i].Args = args
		}
	}
	return out, nil
}

