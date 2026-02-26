package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/skills"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

const (
	// maxToolResultLen is the maximum characters for a single tool result before truncation.
	maxToolResultLen = 30000
)

// Loop is the core agent loop that processes messages through an LLM with tool calling.
type Loop struct {
	provider     provider.Provider
	registry     *toolreg.Registry
	maxIters     int
	systemPrompt string // built from workspace + skills
}

// NewLoop creates a new agent loop with dynamic system prompt.
func NewLoop(p provider.Provider, r *toolreg.Registry, maxIters int, workspacePath string, skillsLoader *skills.Loader) *Loop {
	return &Loop{
		provider:     p,
		registry:     r,
		maxIters:     maxIters,
		systemPrompt: BuildSystemPrompt(workspacePath, skillsLoader),
	}
}

// Process takes a user message and runs the LLM tool-calling loop to produce a response.
func (l *Loop) Process(ctx context.Context, message string) (string, error) {
	messages := []provider.Message{
		{Role: "system", Content: l.systemPrompt},
		{Role: "user", Content: message},
	}

	// Track repeated identical tool failures to break infinite loops.
	type failKey struct{ tool, err string }
	failCounts := make(map[failKey]int)
	const maxRepeatFails = 2

	for iteration := 1; iteration <= l.maxIters; iteration++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		resp, err := l.provider.Chat(messages, l.registry.ToLLMTools())
		if err != nil {
			return "", fmt.Errorf("LLM call failed (iteration %d): %w", iteration, err)
		}

		// No tool calls — final text response.
		if len(resp.ToolCalls) == 0 {
			content := strings.TrimSpace(resp.Content)
			if content == "" {
				if iteration < l.maxIters {
					slog.Warn("empty LLM response, retrying", slog.Int("iteration", iteration))
					continue
				}
				return "", fmt.Errorf("empty model response after %d iterations", iteration)
			}
			return content, nil
		}

		// Append assistant message with tool calls.
		messages = append(messages, provider.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call and append results.
		// Tool responses MUST immediately follow the assistant message with tool_calls
		// (OpenAI/Gemini API requirement — no other messages in between).
		for _, tc := range resp.ToolCalls {
			name := tc.Name
			if name == "" && tc.Function != nil {
				name = tc.Function.Name
			}

			// Parse arguments if not already parsed.
			args := tc.Args
			if args == nil && tc.Function != nil && tc.Function.Arguments != "" {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil {
					args = parsed
				}
			}

			slog.Info("executing tool", slog.String("tool", name), slog.Int("iteration", iteration))

			result, execErr := l.registry.Execute(ctx, name, args)
			if execErr != nil {
				errMsg := execErr.Error()
				result = "Error: " + errMsg
				slog.Warn("tool execution failed", slog.String("tool", name), slog.Any("error", execErr))

				// Detect repeated identical failures — break loop early.
				fk := failKey{tool: name, err: errMsg}
				failCounts[fk]++
				if failCounts[fk] > maxRepeatFails {
					return "", fmt.Errorf("tool %q keeps failing with the same error (%q) after %d attempts — stopping to avoid infinite loop",
						name, errMsg, failCounts[fk])
				}
			}

			// Truncate very large results.
			if len(result) > maxToolResultLen {
				result = result[:maxToolResultLen] + "\n... (truncated)"
			}

			callID := tc.ID
			if callID == "" {
				callID = fmt.Sprintf("call_%d_%s", iteration, name)
			}

			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: callID,
			})
		}

		// Warn the agent when approaching the iteration limit so it can escalate proactively.
		// Injected AFTER tool responses to preserve the required message ordering:
		// assistant(tool_calls) → tool(response) → ... → user(warning).
		const iterWarnThreshold = 5
		if iteration == l.maxIters-iterWarnThreshold {
			slog.Warn("approaching iteration limit, injecting escalation prompt", slog.Int("iteration", iteration))
			messages = append(messages, provider.Message{
				Role: "user",
				Content: fmt.Sprintf(
					"⚠️ SYSTEM: %d/%d iterations used. You have %d iterations left. "+
						"If the task is not progressing, IMMEDIATELY call claude_code(async=true) to escalate "+
						"with: task description, what you tried, and the exact error. "+
						"Read skill 'claude-escalation' for the prompt template.",
					iteration, l.maxIters, iterWarnThreshold,
				),
			})
		}
	}

	return "", fmt.Errorf("max tool iterations reached (%d)", l.maxIters)
}
