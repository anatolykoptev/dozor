package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// ---- mockProvider ----

// mockResponse pairs a provider.Response with an optional error.
type mockResponse struct {
	resp *provider.Response
	err  error
}

// mockProvider implements provider.Provider by returning pre-queued responses in order.
// Once the queue is exhausted every additional call returns an error.
type mockProvider struct {
	responses []mockResponse
	callCount int
}

func (m *mockProvider) Chat(_ []provider.Message, _ []provider.ToolDefinition) (*provider.Response, error) {
	if m.callCount >= len(m.responses) {
		return nil, errors.New("mockProvider: no more responses queued")
	}
	r := m.responses[m.callCount]
	m.callCount++
	return r.resp, r.err
}

// textResp is a convenience constructor for a plain-text response (no tool calls).
func textResp(content string) mockResponse {
	return mockResponse{resp: &provider.Response{Content: content, FinishReason: "stop"}}
}

// toolCallResp returns a response that contains a single tool call.
func toolCallResp(id, name string, args map[string]any) mockResponse {
	return mockResponse{resp: &provider.Response{
		Content: "",
		ToolCalls: []provider.ToolCall{
			{ID: id, Name: name, Args: args},
		},
		FinishReason: "tool_calls",
	}}
}

// errResp returns a response that is a provider-level error.
func errResp(err error) mockResponse {
	return mockResponse{err: err}
}

// ---- mockTool ----

// mockTool is a toolreg.Tool whose Execute result can be overridden per call.
type mockTool struct {
	name    string
	results []struct {
		out string
		err error
	}
	callCount int
}

func (t *mockTool) Name() string                  { return t.name }
func (t *mockTool) Description() string           { return "mock tool " + t.name }
func (t *mockTool) Parameters() map[string]any    { return map[string]any{"type": "object"} }
func (t *mockTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	if t.callCount >= len(t.results) {
		return "", errors.New("mockTool: no more results queued")
	}
	r := t.results[t.callCount]
	t.callCount++
	return r.out, r.err
}

// toolResult is a convenience constructor for mockTool results.
func toolResult(out string, err error) struct{ out string; err error } {
	return struct{ out string; err error }{out, err}
}

// ---- helpers ----

// newLoop creates a Loop with empty workspace/skills so BuildSystemPrompt uses the
// fallback identity string — no disk I/O is performed.
func newTestLoop(p provider.Provider, r *toolreg.Registry, maxIters int) *Loop {
	return NewLoop(p, r, maxIters, "" /* workspacePath */, nil /* skillsLoader */)
}

// registryWith registers the given tools and returns the registry.
func registryWith(tools ...toolreg.Tool) *toolreg.Registry {
	r := toolreg.NewRegistry()
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

// ---- tests ----

// TestProcess_SimpleTextResponse verifies that when the provider returns a plain
// text response (no tool calls), Process returns that text immediately.
func TestProcess_SimpleTextResponse(t *testing.T) {
	p := &mockProvider{responses: []mockResponse{
		textResp("hello world"),
	}}
	l := newTestLoop(p, toolreg.NewRegistry(), 10)

	got, err := l.Process(context.Background(), "ping")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
	if p.callCount != 1 {
		t.Errorf("provider called %d times, want 1", p.callCount)
	}
}

// TestProcess_SingleToolCall verifies the happy-path tool-calling flow:
// provider requests a tool → registry executes it → provider returns text.
func TestProcess_SingleToolCall(t *testing.T) {
	tool := &mockTool{
		name:    "say_hello",
		results: []struct{ out string; err error }{toolResult("tool output", nil)},
	}
	p := &mockProvider{responses: []mockResponse{
		toolCallResp("call_1", "say_hello", map[string]any{"msg": "hi"}),
		textResp("done"),
	}}
	l := newTestLoop(p, registryWith(tool), 10)

	got, err := l.Process(context.Background(), "call the tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "done" {
		t.Errorf("got %q, want %q", got, "done")
	}
	if tool.callCount != 1 {
		t.Errorf("tool called %d times, want 1", tool.callCount)
	}
}

// TestProcess_EmptyResponseRetry verifies that a single empty content response
// causes the loop to retry and succeed on the next call.
func TestProcess_EmptyResponseRetry(t *testing.T) {
	p := &mockProvider{responses: []mockResponse{
		textResp("   "), // whitespace-only → treated as empty
		textResp("real answer"),
	}}
	l := newTestLoop(p, toolreg.NewRegistry(), 10)

	got, err := l.Process(context.Background(), "ask")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "real answer" {
		t.Errorf("got %q, want %q", got, "real answer")
	}
	if p.callCount != 2 {
		t.Errorf("provider called %d times, want 2", p.callCount)
	}
}

// TestProcess_EmptyResponseExhaustsIterations verifies that persistent empty
// responses exhaust maxIters and return an error.
func TestProcess_EmptyResponseExhaustsIterations(t *testing.T) {
	const maxIters = 3
	responses := make([]mockResponse, maxIters)
	for i := range responses {
		responses[i] = textResp("")
	}
	p := &mockProvider{responses: responses}
	l := newTestLoop(p, toolreg.NewRegistry(), maxIters)

	_, err := l.Process(context.Background(), "ask")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "empty model response") {
		t.Errorf("error %q should contain 'empty model response'", err.Error())
	}
}

// TestProcess_ToolExecutionError verifies that a tool error is surfaced as a
// "Error: …" tool result and the loop continues — the provider can still reply.
func TestProcess_ToolExecutionError(t *testing.T) {
	tool := &mockTool{
		name:    "boom",
		results: []struct{ out string; err error }{toolResult("", errors.New("disk full"))},
	}
	p := &mockProvider{responses: []mockResponse{
		toolCallResp("c1", "boom", nil),
		textResp("handled error"),
	}}
	l := newTestLoop(p, registryWith(tool), 10)

	got, err := l.Process(context.Background(), "do it")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "handled error" {
		t.Errorf("got %q, want %q", got, "handled error")
	}
}

// TestProcess_RepeatedToolFailure verifies that the same tool failing with the
// same error maxRepeatFails+1 times breaks the loop with an error.
// maxRepeatFails == 2, so 3 identical failures should trigger the guard.
func TestProcess_RepeatedToolFailure(t *testing.T) {
	const failMsg = "connection refused"
	// Build enough queued results: 3 identical failures, plus a final text
	// response that should never be reached.
	results := make([]struct{ out string; err error }, maxRepeatFails+1)
	for i := range results {
		results[i] = toolResult("", errors.New(failMsg))
	}
	tool := &mockTool{name: "flaky", results: results}

	// Each tool call is preceded by an assistant message requesting it.
	responses := make([]mockResponse, maxRepeatFails+1)
	for i := range responses {
		responses[i] = toolCallResp("c", "flaky", nil)
	}
	// Append a text response that should NOT be reached.
	responses = append(responses, textResp("should not reach"))

	p := &mockProvider{responses: responses}
	l := newTestLoop(p, registryWith(tool), 20)

	_, err := l.Process(context.Background(), "go")
	if err == nil {
		t.Fatal("expected error due to repeated tool failure, got nil")
	}
	if !strings.Contains(err.Error(), "keeps failing") {
		t.Errorf("error %q should mention 'keeps failing'", err.Error())
	}
	if !strings.Contains(err.Error(), "flaky") {
		t.Errorf("error %q should contain the tool name 'flaky'", err.Error())
	}
}

// TestProcess_ContextCancelled verifies that a cancelled context causes the loop
// to return ctx.Err() on the iteration boundary check.
//
// Flow:
//  1. iteration 1: provider returns a tool call
//  2. the tool cancels ctx during Execute — this simulates slow work that outlasts the deadline
//  3. iteration 2: the `select { case <-ctx.Done() }` fires → loop returns ctx.Err()
func TestProcess_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // safety: prevent goroutine leak if test fails early

	// cancellingTool cancels the context when it executes.
	cancellingTool := &hookTool{
		name: "slow",
		execute: func(_ context.Context, _ map[string]any) (string, error) {
			cancel() // fire cancellation while the tool "runs"
			return "ok", nil
		},
	}

	p := &mockProvider{responses: []mockResponse{
		toolCallResp("c1", "slow", nil),
		// iteration 2 never reaches Chat because ctx.Done() fires first.
		textResp("too late"),
	}}

	l := newTestLoop(p, registryWith(cancellingTool), 10)
	_, err := l.Process(ctx, "work")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// hookTool is a Tool whose Execute function is provided at construction time.
type hookTool struct {
	name    string
	execute func(context.Context, map[string]any) (string, error)
}

func (h *hookTool) Name() string               { return h.name }
func (h *hookTool) Description() string        { return "hook tool " + h.name }
func (h *hookTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (h *hookTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return h.execute(ctx, args)
}

// TestProcess_LLMError verifies that a provider.Chat error is wrapped and returned.
func TestProcess_LLMError(t *testing.T) {
	provErr := errors.New("rate limit exceeded")
	p := &mockProvider{responses: []mockResponse{errResp(provErr)}}
	l := newTestLoop(p, toolreg.NewRegistry(), 10)

	_, err := l.Process(context.Background(), "ask")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, provErr) {
		t.Errorf("expected wrapped provErr, got: %v", err)
	}
	if !strings.Contains(err.Error(), "LLM call failed") {
		t.Errorf("error %q should contain 'LLM call failed'", err.Error())
	}
}

// TestProcess_MaxIterationsReached verifies that an unending sequence of tool
// calls eventually triggers the "max tool iterations" error.
func TestProcess_MaxIterationsReached(t *testing.T) {
	const maxIters = 5
	tool := &mockTool{name: "loop_tool"}
	// Each iteration the provider asks for the same tool — fill enough results.
	for i := 0; i < maxIters; i++ {
		tool.results = append(tool.results, toolResult("ok", nil))
	}

	responses := make([]mockResponse, maxIters)
	for i := range responses {
		responses[i] = toolCallResp("c", "loop_tool", nil)
	}
	p := &mockProvider{responses: responses}
	l := newTestLoop(p, registryWith(tool), maxIters)

	_, err := l.Process(context.Background(), "loop")
	if err == nil {
		t.Fatal("expected max iterations error, got nil")
	}
	if !strings.Contains(err.Error(), "max tool iterations") {
		t.Errorf("error %q should contain 'max tool iterations'", err.Error())
	}
}

// TestProcess_ToolResultTruncation verifies that a tool result exceeding
// maxToolResultLen characters is truncated to exactly maxToolResultLen chars
// plus the truncation suffix.
func TestProcess_ToolResultTruncation(t *testing.T) {
	bigOutput := strings.Repeat("x", maxToolResultLen+1000)
	tool := &mockTool{
		name:    "bigdata",
		results: []struct{ out string; err error }{toolResult(bigOutput, nil)},
	}

	// Capture the tool message content by inspecting what the provider receives.
	var capturedToolMsg string
	capturingProvider := &capturingProvider{
		onChat: func(msgs []provider.Message) {
			// After tool execution, the last message is the tool result.
			for _, m := range msgs {
				if m.Role == "tool" {
					capturedToolMsg = m.Content
				}
			}
		},
		inner: &mockProvider{responses: []mockResponse{
			toolCallResp("c1", "bigdata", nil),
			textResp("done"),
		}},
	}

	l := newTestLoop(capturingProvider, registryWith(tool), 10)
	_, err := l.Process(context.Background(), "fetch big data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedToolMsg) > maxToolResultLen+len("\n... (truncated)") {
		t.Errorf("tool result not truncated: length %d", len(capturedToolMsg))
	}
	if !strings.HasSuffix(capturedToolMsg, "... (truncated)") {
		t.Errorf("truncated result should end with '... (truncated)', got suffix: %q",
			capturedToolMsg[max(0, len(capturedToolMsg)-30):])
	}
	// The prefix must be exactly maxToolResultLen 'x' characters.
	if capturedToolMsg[:maxToolResultLen] != strings.Repeat("x", maxToolResultLen) {
		t.Error("first maxToolResultLen chars of tool result should be original content")
	}
}

// capturingProvider calls onChat before delegating to the inner provider.
type capturingProvider struct {
	onChat func([]provider.Message)
	inner  *mockProvider
}

func (c *capturingProvider) Chat(msgs []provider.Message, tools []provider.ToolDefinition) (*provider.Response, error) {
	c.onChat(msgs)
	return c.inner.Chat(msgs, tools)
}

// max returns the larger of two ints (stdlib min/max available since Go 1.21).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
