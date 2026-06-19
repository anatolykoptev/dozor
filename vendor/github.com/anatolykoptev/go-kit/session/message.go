package session

// FunctionCall is the OpenAI-style nested function call format.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents a single tool invocation in a message.
type ToolCall struct {
	ID       string        `json:"id"`
	Name     string        `json:"name,omitempty"`
	Args     string        `json:"arguments,omitempty"`
	Function *FunctionCall `json:"function,omitempty"`
}

// Message is a provider-agnostic chat message.
//
// ChatTime, MessageID, and Name mirror MemDB's ingest schema and
// go-kit/llm v0.43+ Message — keeping persisted history aligned with
// the format used in flight. All three are omitempty: existing
// session files written before this revision deserialise without
// changes.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`

	// ChatTime is the message timestamp in RFC3339-UTC format.
	// Use go-kit/llm.FormatChatTime(time.Time) to produce a value
	// that round-trips cleanly across kitllm, MemDB, and this store.
	ChatTime string `json:"chat_time,omitempty"`

	// MessageID is a stable per-message identifier (MemDB dedup key).
	MessageID string `json:"message_id,omitempty"`

	// Name is an optional speaker label (OpenAI-native; MemDB-honoured).
	Name string `json:"name,omitempty"`
}
