package provider

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	"github.com/anatolykoptev/go-kit/tracing"
	"github.com/anatolykoptev/go-kit/tracing/httpmw"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	return newOpenAIWithConfig(apiURL, apiKey, model, maxItersFromEnv()), true
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
func (o *OpenAI) Chat(ctx context.Context, messages []kitllm.Message, tools []kitllm.Tool) (*kitllm.ChatResponse, error) {
	return o.chatWithRetry(ctx, messages, tools)
}

func (o *OpenAI) chatWithRetry(ctx context.Context, messages []kitllm.Message, tools []kitllm.Tool) (*kitllm.ChatResponse, error) {
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
// Returns false when the error is an auth failure, a non-transient error,
// or when attempt has reached chatMaxRetries.
//
// Classification:
//   - context.Canceled / DeadlineExceeded — never retry.
//   - IsAuth (401/403) — never retry; same bad key will fail again.
//   - kitllm.APIError non-transient (400, 404, etc.) — no retry.
//   - IsTransient (429, 5xx) — retry with backoff.
//   - non-APIError (transport, decode errors) — treated as network-class
//     transient; retry with backoff.
func shouldRetry(err error, attempt int) (bool, time.Duration) {
	// Context cancellation / deadline — never retry; the caller's ctx is gone.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	// Auth errors — fail immediately (retrying with the same bad key buys nothing).
	if IsAuth(err) {
		return false, 0
	}

	var ae *kitllm.APIError
	if errors.As(err, &ae) {
		// Known API error: only retry on transient status codes (429, 5xx).
		if IsTransient(err) && attempt < chatMaxRetries {
			delay := chatBackoff(attempt)
			if IsRateLimit(err) {
				slog.Warn("LLM rate limit, retrying",
					slog.Int("attempt", attempt+1),
					slog.Duration("delay", delay))
			} else {
				slog.Warn("LLM server error, retrying",
					slog.Int("status", ae.StatusCode),
					slog.Int("attempt", attempt+1),
					slog.Duration("delay", delay))
			}
			return true, delay
		}
		return false, 0
	}

	// Non-APIError (transport failures, JSON decode, empty-choices): treat as
	// network-class transient — retry while attempts remain.
	if attempt >= chatMaxRetries {
		return false, 0
	}
	delay := chatBackoff(attempt)
	slog.Warn("LLM network error, retrying",
		slog.Int("attempt", attempt+1),
		slog.Duration("delay", delay))
	return true, delay
}

// TODO(kit-v0.63): kitllm.APIError does not surface the HTTP Retry-After header
// (v0.62.0 fields: StatusCode, Body, Type, Retryable). Once upstreamed, parse
// APIError.RetryAfter here and substitute it for exponential backoff on 429.
// PR4 dropped the Google retryDelay metadata path; this is the way back.
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
// jitter, and free-function classification over kitllm.APIError.
//
// kitllm types are passed through directly — no adapter conversion needed.
func (o *OpenAI) doChatCtx(ctx context.Context, messages []kitllm.Message, tools []kitllm.Tool) (*kitllm.ChatResponse, error) {
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
		opts = append(opts, kitllm.WithTools(tools))
		opts = append(opts, kitllm.WithToolChoice("auto"))
	}

	resp, err := client.Chat(ctx, messages, opts...)
	if err != nil {
		// kitllm.APIError flows through as-is -- shouldRetry classifies via
		// IsAuth/IsTransient free functions. Non-API errors (transport,
		// empty-choices, JSON decode) are treated as network-class transient.
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

	return resp, nil
}

// newOpenAIWithConfig constructs an *OpenAI directly from explicit values
// (url, key, model, maxIters) rather than reading from env. Used by
// newFallbackFromEnv to build the fallback provider without a type
// assertion into the primary — both callers compose via the Provider
// interface only.
func newOpenAIWithConfig(url, key, model string, maxIters int) *OpenAI {
	return &OpenAI{
		apiURL:   url,
		apiKey:   key,
		model:    model,
		maxIters: maxIters,
		client:   &http.Client{Timeout: 90 * time.Second, Transport: httpmw.WrapTransport(&http.Transport{})},
	}
}

// maxItersFromEnv reads DOZOR_MAX_TOOL_ITERATIONS, returning 10 as default.
// Shared by NewOpenAI and newFallbackFromEnv so both honour the same env.
func maxItersFromEnv() int {
	if v := os.Getenv("DOZOR_MAX_TOOL_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 10
}
