package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// OpenAI is an OpenAI-compatible HTTP provider.
type OpenAI struct {
	apiURL   string
	apiKey   string
	model    string
	client   *http.Client
	maxIters int
}

// NewOpenAI creates a provider from environment variables.
// DOZOR_LLM_URL (default http://127.0.0.1:8787/v1)
// DOZOR_LLM_MODEL (default gemini-2.5-flash)
// DOZOR_LLM_API_KEY
// DOZOR_MAX_TOOL_ITERATIONS (default 10)
func NewOpenAI() *OpenAI {
	apiURL := os.Getenv("DOZOR_LLM_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8787/v1"
	}
	model := os.Getenv("DOZOR_LLM_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}
	maxIters := 10
	if v := os.Getenv("DOZOR_MAX_TOOL_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxIters = n
		}
	}
	return &OpenAI{
		apiURL:   apiURL,
		apiKey:   os.Getenv("DOZOR_LLM_API_KEY"),
		model:    model,
		maxIters: maxIters,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// MaxIterations returns the configured max tool call iterations.
func (o *OpenAI) MaxIterations() int { return o.maxIters }

// Chat sends a chat completion request and returns the response.
func (o *OpenAI) Chat(messages []Message, tools []ToolDefinition) (*Response, error) {
	body := map[string]any{
		"model":    o.model,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", o.apiURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseProviderError(resp.StatusCode, data)
	}

	var result chatCompletionResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("empty choices in LLM response")
	}

	choice := result.Choices[0]
	out := &Response{
		Content:      choice.Message.Content,
		FinishReason: choice.FinishReason,
	}

	for _, tc := range choice.Message.ToolCalls {
		call := ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: &FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
		// Pre-parse arguments for convenience.
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err == nil {
			call.Args = args
		}
		call.Name = tc.Function.Name
		out.ToolCalls = append(out.ToolCalls, call)
	}

	return out, nil
}

// OpenAI API response types.
type chatCompletionResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []apiToolCall  `json:"tool_calls,omitempty"`
}

type apiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
