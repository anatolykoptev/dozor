package provider

// Message represents a chat message in the LLM conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
	Chat(messages []Message, tools []ToolDefinition) (*Response, error)
}
