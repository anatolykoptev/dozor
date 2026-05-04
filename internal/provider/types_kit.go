package provider

import (
	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// Bidirectional converters between dozor's provider package types and
// go-kit/llm types. Used by openai.go to delegate HTTP/JSON mechanics
// to kitllm while keeping the public Provider interface stable for
// callers in internal/agent and cmd/dozor/setup.go.

// toKitMessages converts dozor messages to kitllm messages. The Content
// field in kitllm.Message is `any` (string OR []ContentPart for
// multimodal); dozor uses string-only today, so we set Content directly.
func toKitMessages(msgs []Message) []kitllm.Message {
	out := make([]kitllm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = kitllm.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolCalls:  toKitToolCalls(m.ToolCalls),
			ChatTime:   m.ChatTime,
			MessageID:  m.MessageID,
			Name:       m.Name,
		}
	}
	return out
}

// toKitTools converts dozor tool definitions to kitllm.Tool slice.
func toKitTools(tools []ToolDefinition) []kitllm.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]kitllm.Tool, len(tools))
	for i, t := range tools {
		out[i] = kitllm.Tool{
			Type: t.Type,
			Function: kitllm.ToolFunction{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		}
	}
	return out
}

// toKitToolCalls maps dozor's hybrid ToolCall (Function pointer OR flat
// Name+Args fields, depending on history origin) into kitllm's flat
// ToolCall struct. Empty input returns nil to keep the request body
// terse.
func toKitToolCalls(calls []ToolCall) []kitllm.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]kitllm.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = kitllm.ToolCall{
			ID:   c.ID,
			Type: c.Type,
		}
		if c.Function != nil {
			out[i].Function = kitllm.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			}
		}
	}
	return out
}

// fromKitResponse converts a kitllm.ChatResponse into dozor's Response.
// The kitllm response is already deserialised — we re-shape and copy
// tool calls into dozor's hybrid ToolCall (Function pointer form, which
// is what the existing agent loop expects).
func fromKitResponse(r *kitllm.ChatResponse) *Response {
	if r == nil {
		return nil
	}
	return &Response{
		Content:      r.Content,
		ToolCalls:    fromKitToolCalls(r.ToolCalls),
		FinishReason: r.FinishReason,
	}
}

// fromKitToolCalls maps kitllm's flat ToolCall into dozor's pointered
// FunctionCall form. Returns nil for empty input.
func fromKitToolCalls(calls []kitllm.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, c := range calls {
		fc := c.Function // copy
		out[i] = ToolCall{
			ID:   c.ID,
			Type: c.Type,
			Function: &FunctionCall{
				Name:      fc.Name,
				Arguments: fc.Arguments,
			},
		}
	}
	return out
}
