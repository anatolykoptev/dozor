package provider

import "context"

// Message represents a chat message in the LLM conversation.
//
// ChatTime, MessageID, and Name mirror the MemDB ingest schema (memdb-go
// api/openapi.yaml: ChatCompletion*MessageParam). Empty strings are
// invisible on the wire (omitempty); pair Message.ChatTime with the
// kitllm.WithMessageTimestamps option in openai.go to surface the
// timestamp inside the LLM's actual context.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`

	// ChatTime is RFC3339-UTC. Use kitllm.FormatChatTime(time.Time) to
	// produce values that round-trip cleanly to MemDB.
	ChatTime string `json:"chat_time,omitempty"`

	// MessageID is a stable per-message identifier for MemDB dedup.
	MessageID string `json:"message_id,omitempty"`

	// Name is an optional speaker label (OpenAI- and MemDB-honoured).
	Name string `json:"name,omitempty"`
}

// ToolCall represents an LLM-requested tool invocation.
type ToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type,omitempty"`
	Function *FunctionCall  `json:"function,omitempty"`
	Name     string         `json:"name,omitempty"`
	Args     map[string]any `json:"arguments,omitempty"`
}

// FunctionCall is the function name + raw JSON arguments from the LLM.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string
}

// Response is the LLM's reply to a chat request.
type Response struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason"`
}

// ToolDefinition is an OpenAI-compatible function tool schema.
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition describes a callable function for the LLM.
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Provider is the interface for LLM backends.
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error)
}
