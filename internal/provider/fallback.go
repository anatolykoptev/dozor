package provider

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/go-kit/hedge"
)

// withFallback wraps a primary Provider and retries on any error with a fallback.
type withFallback struct {
	primary  Provider
	fallback Provider
}

// NewFromEnv creates a Provider from environment variables.
// If DOZOR_LLM_FALLBACK_URL (or DOZOR_LLM_FALLBACK_API_KEY) is set,
// a fallback provider is chained after the primary.
func NewFromEnv() *withFallback {
	primaryOpenAI := NewOpenAI()
	primary := Provider(primaryOpenAI)

	fallbackURL := os.Getenv("DOZOR_LLM_FALLBACK_URL")
	fallbackKey := os.Getenv("DOZOR_LLM_FALLBACK_API_KEY")
	fallbackModel := os.Getenv("DOZOR_LLM_FALLBACK_MODEL")

	if fallbackURL == "" && fallbackKey == "" {
		return &withFallback{primary: primaryOpenAI, fallback: nil}
	}

	// Inherit primary URL/model if not explicitly overridden.
	if fallbackURL == "" {
		fallbackURL = os.Getenv("DOZOR_LLM_URL")
		if fallbackURL == "" {
			fallbackURL = "http://127.0.0.1:8787/v1"
		}
	}
	if fallbackModel == "" {
		fallbackModel = os.Getenv("DOZOR_LLM_MODEL")
		if fallbackModel == "" {
			fallbackModel = "gemini-2.5-flash"
		}
	}
	if fallbackKey == "" {
		fallbackKey = os.Getenv("DOZOR_LLM_API_KEY")
	}

	maxIters := primaryOpenAI.MaxIterations()

	fb := &OpenAI{
		apiURL:   fallbackURL,
		apiKey:   fallbackKey,
		model:    fallbackModel,
		maxIters: maxIters,
		client:   primaryOpenAI.client,
	}

	slog.Info("LLM fallback provider configured", //nolint:gosec // no log injection
		slog.String("url", fallbackURL),
		slog.String("model", fallbackModel))

	return &withFallback{primary: primary, fallback: fb}
}

// MaxIterations delegates to the primary provider's iteration limit.
func (w *withFallback) MaxIterations() int {
	if p, ok := w.primary.(interface{ MaxIterations() int }); ok {
		return p.MaxIterations()
	}
	return 10
}

// Chat tries primary; if it is slow OR fails, races a fallback in
// parallel using hedge.DoFallback. The first success wins.
//
// Hedge delay is configured by DOZOR_LLM_HEDGE_DELAY (Go duration string,
// default "3s"). Set "0" to disable hedging entirely — Chat then falls
// back to the historical sequential behaviour (primary, then fallback
// only on primary error). Use "0" when you care more about $$ than tail
// latency: under healthy primary, hedging never starts the fallback, so
// cost stays the same; but a misconfiguration could change that.
//
// Primary auth errors (401/403) still short-circuit without invoking
// fallback — re-running with the same misconfigured key buys nothing,
// and we'd rather surface the auth failure than mask it with a
// fallback-success.
func (w *withFallback) Chat(messages []Message, tools []ToolDefinition) (*Response, error) {
	if w.fallback == nil {
		return w.primary.Chat(messages, tools)
	}

	hedgeDelay := hedgeDelayFromEnv()
	if hedgeDelay <= 0 {
		// Sequential fallback: primary first, fallback only on error.
		return w.chatSequential(messages, tools)
	}

	primaryFn := func(_ context.Context) (*Response, error) {
		return w.primary.Chat(messages, tools)
	}
	fallbackFn := func(_ context.Context) (*Response, error) {
		slog.Info("LLM fallback engaged",
			slog.Duration("hedge.delay", hedgeDelay))
		return w.fallback.Chat(messages, tools)
	}
	return hedge.DoFallback(context.Background(), hedgeDelay, primaryFn, fallbackFn)
}

// chatSequential preserves the historical primary→fallback-on-error
// behaviour for cost-conscious deployments.
func (w *withFallback) chatSequential(messages []Message, tools []ToolDefinition) (*Response, error) {
	resp, err := w.primary.Chat(messages, tools)
	if err == nil {
		return resp, nil
	}
	if isAuthError(err) {
		return nil, err
	}
	slog.Warn("primary LLM failed, trying fallback",
		slog.String("error", err.Error()))
	return w.fallback.Chat(messages, tools)
}

// hedgeDelayFromEnv reads DOZOR_LLM_HEDGE_DELAY as a Go duration.
// Default 3s. Returns 0 if explicitly set to "0" or "off" so callers
// can disable hedging without parsing the value themselves.
func hedgeDelayFromEnv() time.Duration {
	v := os.Getenv("DOZOR_LLM_HEDGE_DELAY")
	switch v {
	case "":
		return 3 * time.Second
	case "0", "off", "disabled":
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return 3 * time.Second
}

// isAuthError returns true if err is a 401 or 403 ProviderError.
func isAuthError(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.IsAuth()
}
