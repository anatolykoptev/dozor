package provider

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	"github.com/anatolykoptev/go-kit/retry"
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
	apiURL        string
	apiKey        string
	model         string
	fallbackChain []string // optional cross-provider model chain (via cliproxyapi); empty = single-model
	client        *http.Client
	chainClient   *kitllm.Client        // shared across calls so cooldown state persists; nil = no fallback chain
	modelRegistry *kitllm.ModelRegistry // health-aware registry; nil = no chain
	maxIters      int
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
	// CSV cross-provider chain. DOZOR_LLM_MODEL_FALLBACK takes precedence;
	// fall through to fleet-wide LLM_MODEL_FALLBACK когда не задано (consistent
	// SOT с другими сервисами через config/llm.env). Sanitized + deduped by kit.
	chainCSV := os.Getenv("DOZOR_LLM_MODEL_FALLBACK")
	if chainCSV == "" {
		chainCSV = os.Getenv("LLM_MODEL_FALLBACK")
	}
	chain := kitllm.ParseModelFallbackChain(chainCSV)
	// Strip primary model out of chain to avoid duplicate first attempt.
	if len(chain) > 0 {
		out := chain[:0]
		for _, m := range chain {
			if m != model {
				out = append(out, m)
			}
		}
		chain = out
	}
	o := newOpenAIWithConfig(apiURL, apiKey, model, maxItersFromEnv())
	o.fallbackChain = chain
	if len(chain) > 0 {
		// ModelRegistry is created once here and stored on the struct so that
		// its /v1/models cache (TTL: 5 min) is shared across all buildChainClient
		// calls and across requests. Creating it per-call would defeat the cache.
		o.modelRegistry = kitllm.NewModelRegistry()
		o.chainClient = o.buildChainClient()
	}
	return o, true
}

// MaxIterations returns the configured max tool call iterations.
func (o *OpenAI) MaxIterations() int { return o.maxIters }

// Model returns the primary model id for structured logging.
func (o *OpenAI) Model() string { return o.model }

// chatRetryOpts are the retry options for Chat. 4 total attempts (1 initial
// + 3 retries), exponential 2s→30s. Jitter is disabled so that the
// server-supplied Retry-After delay is honoured exactly — kit/retry applies
// jitter uniformly to both the Retry-After and the exponential delays, which
// can bring a 1s Retry-After below its nominal value and break the
// TestChatWithRetry_HonoursRetryAfter timing assertion. Non-transient errors
// (auth, 4xx client, context cancellation) abort immediately via RetryIf.
var chatRetryOpts = retry.Options{
	MaxAttempts:  4, // 1 initial + 3 retries
	InitialDelay: 2 * time.Second,
	MaxDelay:     30 * time.Second,
	Jitter:       false, // see comment above; was ±25% (chatJitterDivisor=4) in prior code
	RetryIf:      IsTransient,
}

// Chat sends a chat completion request and returns the response.
// Retries up to 3 times on transient errors (429, 5xx, network failures)
// using kit/retry exponential backoff with ±25% jitter.
//
// Non-transient errors (401, 403, 4xx client, context cancellation/deadline)
// abort immediately — IsTransient returns false for these, so RetryIf stops
// the retry loop on the first attempt.
//
// When the LLM provider sends a Retry-After header (e.g. on 429), the
// server-suggested delay is honoured via retry.RetryAfter wrapping.
func (o *OpenAI) Chat(ctx context.Context, messages []kitllm.Message, tools []kitllm.Tool) (*kitllm.ChatResponse, error) {
	ctx, span := tracing.Start(ctx, "llm.chat",
		attribute.String("llm.model", o.model),
		attribute.String("llm.url", o.apiURL),
		attribute.Int("llm.messages.count", len(messages)),
		attribute.Int("llm.tools.count", len(tools)))
	defer span.End()

	opts := chatRetryOpts
	opts.OnRetry = func(attempt int, err error) {
		// OnRetry fires before kit/retry's RetryIf check, so it is called
		// even for errors that will not be retried (e.g. auth errors when
		// IsTransient returns false). Guard to avoid misleading log lines.
		if !IsTransient(err) {
			return
		}
		var ae *kitllm.APIError
		switch {
		case IsRateLimit(err):
			slog.Warn("LLM rate limit, retrying",
				slog.Int("attempt", attempt+1))
		case errors.As(err, &ae):
			slog.Warn("LLM server error, retrying",
				slog.Int("status", ae.StatusCode),
				slog.Int("attempt", attempt+1))
		default:
			slog.Warn("LLM network error, retrying",
				slog.Int("attempt", attempt+1))
		}
	}

	resp, err := retry.Do(ctx, opts, func() (*kitllm.ChatResponse, error) {
		r, callErr := o.doChatCtx(ctx, messages, tools)
		if callErr != nil {
			// If the LLM provider sent Retry-After, honour it for the next
			// attempt instead of the exponential schedule.
			var ae *kitllm.APIError
			if errors.As(callErr, &ae) && ae.RetryAfter > 0 {
				return nil, retry.RetryAfter(ae.RetryAfter, callErr)
			}
			return nil, callErr
		}
		return r, nil
	})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	span.SetAttributes(
		attribute.String("llm.finish_reason", resp.FinishReason),
		attribute.Int("llm.response.length", len(resp.Content)),
		attribute.Int("llm.tool_calls.count", len(resp.ToolCalls)))
	return resp, nil
}

// cooldownDuration resolves the per-model cooldown TTL from the environment.
// LLM_COOLDOWN_SECONDS is read as an integer number of seconds (no DOZOR_ prefix
// — fleet-wide single knob). Falls back to 5 minutes when unset or ≤0.
func cooldownDuration() time.Duration {
	if v := os.Getenv("LLM_COOLDOWN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 5 * time.Minute
}

// buildChainClient constructs the shared chain client with per-model cooldown
// and health-aware model filtering.
//
// Called once at construction (NewOpenAI); the returned client is reused across
// requests so cooldown state accumulates across calls (the key property that
// prevents dead-hop RTTs on quota-exhausted free-tier models).
//
// Model filtering: BuildModelChainEndpointsFiltered probes {apiURL}/v1/models
// once (TTL-cached on o.modelRegistry) and drops any chain entry absent from
// the live set before constructing the endpoint list. Graceful degradation:
// if the registry is nil, the fetch fails, the response is empty, or filtering
// would empty the chain, the FULL unfiltered chain is used — no new failure mode.
//
// The observer logs a Warn on:
//   - cooldown ENTRY (per-model quota exhaustion visible in the journal)
//   - filter event: any dropped models or a degraded-registry fallback
//
// Both observers fire async inside kit goroutines and must not block or panic.
// slog.Warn without context is intentional — there is no per-request ctx at
// observer firing time.
func (o *OpenAI) buildChainClient() *kitllm.Client {
	// modelFilterObserver is called once per buildChainClient invocation.
	// It logs dropped models (a dead upstream model was removed from the
	// chain) and degradation events (the live model set could not be
	// obtained — fallback to the full chain).
	//
	// dozor has no Prometheus surface in the provider package, so slog is
	// the right output here. One log line per chain build is acceptable:
	// buildChainClient is called exactly once at startup (see NewOpenAI).
	modelFilterObserver := func(ev kitllm.ModelFilterEvent) {
		if ev.Degraded {
			slog.Warn("llm model filter degraded — using full chain",
				slog.String("reason", ev.Reason),
				slog.String("base_url", ev.BaseURL),
				slog.Int("chain_len", ev.Requested),
				slog.Int("available", ev.Available))
			return
		}
		if len(ev.Dropped) > 0 {
			slog.Warn("llm model filter dropped dead models",
				slog.Any("dropped", ev.Dropped),
				slog.String("base_url", ev.BaseURL),
				slog.Int("requested", ev.Requested),
				slog.Int("kept", ev.Kept),
				slog.Int("available", ev.Available))
		}
	}

	eps := kitllm.BuildModelChainEndpointsFiltered(
		context.Background(),
		o.modelRegistry,
		o.apiURL, o.apiKey, o.model, o.fallbackChain,
		modelFilterObserver,
	)
	return kitllm.NewClient(o.apiURL, o.apiKey, o.model,
		kitllm.WithHTTPClient(o.client),
		kitllm.WithMaxRetries(1), // dozor owns retry loop; 1 = single attempt per endpoint
		kitllm.WithEndpoints(eps),
		// Per-model cooldown: after 2 observed quota-class failures (429 or
		// quota-flagged 503) a model is skipped for cooldownDuration() (or
		// Retry-After if provided), preventing dead-hop RTTs on quota-exhausted
		// free-tier models. TTL = LLM_COOLDOWN_SECONDS (default 5m).
		kitllm.WithModelCooldown(kitllm.CooldownConfig{Default: cooldownDuration()}),
		// Observer: logs a Warn when a model enters cooldown so quota pressure
		// is visible in the journal without polling /metrics.
		kitllm.WithModelCooldownObserver(func(model string, cooling bool, d time.Duration) {
			if cooling {
				slog.Warn("llm model cooldown",
					slog.String("model", model),
					slog.Duration("dur", d))
			}
		}),
	)
}

// doChatCtx delegates HTTP/JSON mechanics to kitllm.Client.Chat. The
// retry/classification policy lives in Chat (via kit/retry.Do).
//
// kitllm's internal retry is disabled (WithMaxRetries(1) = single
// attempt) because dozor owns the retry loop with its own backoff,
// jitter, and free-function classification over kitllm.APIError.
//
// kitllm types are passed through directly — no adapter conversion needed.
func (o *OpenAI) doChatCtx(ctx context.Context, messages []kitllm.Message, tools []kitllm.Tool) (*kitllm.ChatResponse, error) {
	// Use the shared chain client when a fallback chain is configured so that
	// per-model cooldown state accumulates across requests (cross-request memory).
	// Fall back to a fresh single-endpoint client for the no-chain path.
	var client *kitllm.Client
	if o.chainClient != nil {
		client = o.chainClient
	} else {
		client = kitllm.NewClient(o.apiURL, o.apiKey, o.model,
			kitllm.WithHTTPClient(o.client),
			kitllm.WithMaxRetries(1),
		)
	}

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
		// kitllm.APIError flows through as-is — IsTransient classifies via
		// IsAuth/IsRateLimit/IsServerError. Non-API errors (transport,
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
