package provider

import (
	"context"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// Provider is the interface for LLM backends.
//
// Chat takes kitllm types directly — no dozor-specific wrapper needed since
// go-kit/llm.Message gained ChatTime/MessageID/Name in v0.53 and
// kitllm.Tool covers all field types dozor previously duplicated.
type Provider interface {
	Chat(ctx context.Context, messages []kitllm.Message, tools []kitllm.Tool) (*kitllm.ChatResponse, error)
}

// MaxIterationsProvider is the optional interface that lets withFallback
// surface DOZOR_MAX_TOOL_ITERATIONS from the underlying *OpenAI.
type MaxIterationsProvider interface {
	MaxIterations() int
}
