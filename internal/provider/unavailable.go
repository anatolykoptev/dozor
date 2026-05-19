package provider

import (
	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// ErrUnavailable is returned by Providers that are intentionally inert —
// e.g. when no LLM API key is configured. Callers (agent loop, summarize)
// should detect this via errors.Is and surface a structured "LLM
// unavailable" response instead of treating it as a request-time failure.
//
// Value is identical to kitllm.ErrUnavailable; consumers may check
// against either symbol.
var ErrUnavailable = kitllm.ErrUnavailable

// unavailable is a Provider implementation that always returns
// ErrUnavailable from Chat. Used when DOZOR_LLM_API_KEY is empty.
type unavailable struct{}

func (unavailable) Chat(messages []Message, tools []ToolDefinition) (*Response, error) {
	return nil, ErrUnavailable
}

// MaxIterations satisfies the optional interface used by withFallback to
// surface the max-tool-iterations setting. An unavailable provider has no
// iterations to give; return 0 so the agent loop short-circuits cleanly.
func (unavailable) MaxIterations() int { return 0 }

// Compile-time assert that unavailable satisfies the optional interface.
var _ interface{ MaxIterations() int } = unavailable{}
