package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// TestProvider_ToolCallRoundTrip pins the externally-observable round-trip:
// a tool-call response from the LLM server is parsed correctly by the
// provider and reaches the agent loop with intact Name + Arguments. This
// test exists across the type-adapter delete: it must pass before and after.
//
// In PR3 it is the RED signal — it will fail to compile when Provider.Chat
// still accepts dozor's own Message/ToolDefinition types, and will pass
// (GREEN) once the signature switches to kitllm types.
func TestProvider_ToolCallRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "",
					"tool_calls": [{
						"id": "tc_123",
						"type": "function",
						"function": {
							"name": "search",
							"arguments": "{\"query\":\"dozor health\",\"limit\":5}"
						}
					}]
				},
				"finish_reason": "tool_calls"
			}]
		}`))
	}))
	defer srv.Close()

	t.Setenv("DOZOR_LLM_URL", srv.URL)
	t.Setenv("DOZOR_LLM_MODEL", "test-model")
	t.Setenv("DOZOR_LLM_API_KEY", "k")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("NewOpenAI returned ok=false")
	}

	// After PR3: Provider.Chat accepts kitllm types directly.
	// Before PR3: this line fails to compile (type mismatch) — that is the RED signal.
	msgs := []kitllm.Message{{Role: "user", Content: "find docs"}}
	tools := []kitllm.Tool{{
		Type: "function",
		Function: kitllm.ToolFunction{
			Name:        "search",
			Description: "search docs",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	resp, err := p.Chat(context.Background(), msgs, tools)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}

	// kitllm.ChatResponse is flat: resp.ToolCalls (no Choices wrapper).
	if len(resp.ToolCalls) == 0 {
		t.Fatal("no tool calls in response")
	}
	tc := resp.ToolCalls[0]
	if tc.Function.Name != "search" {
		t.Fatalf("Function.Name = %q, want %q", tc.Function.Name, "search")
	}
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("args unmarshal: %v", err)
	}
	if args.Query != "dozor health" {
		t.Fatalf("args.Query = %q, want %q", args.Query, "dozor health")
	}
	if args.Limit != 5 {
		t.Fatalf("args.Limit = %d, want 5", args.Limit)
	}

	// Also verify the standard ChatResponse fields are populated.
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}
	_ = os.Getenv // suppress import if unused
}
