package provider

import (
	"errors"
	"log/slog"
	"os"
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

// Chat tries the primary provider; on any error tries the fallback.
func (w *withFallback) Chat(messages []Message, tools []ToolDefinition) (*Response, error) {
	resp, err := w.primary.Chat(messages, tools)
	if err == nil {
		return resp, nil
	}

	if w.fallback == nil {
		return nil, err
	}

	slog.Warn("primary LLM failed, trying fallback",
		slog.String("error", err.Error()),
		slog.Bool("auth_error", isAuthError(err)))

	resp, fallbackErr := w.fallback.Chat(messages, tools)
	if fallbackErr != nil {
		return nil, fallbackErr
	}
	return resp, nil
}

// isAuthError returns true if err is a 401 or 403 ProviderError.
func isAuthError(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.IsAuth()
}
