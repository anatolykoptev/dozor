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
	maxToolResultLen  = 30000
	maxRepeatFails    = 2
	iterWarnThreshold = 5
)

type failKey struct{ tool, err string }

// Loop is the core agent loop that processes messages through an LLM with tool calling.
type Loop struct {
	provider     provider.Provider
	registry     *toolreg.Registry
	maxIters     int
	systemPrompt string // built from workspace + skills
	sessions     *SessionStore
}

// WithSessions attaches a SessionStore for conversation history.
func (l *Loop) WithSessions(s *SessionStore) *Loop {
	l.sessions = s
	return l
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
// When sessionKey is non-empty and a SessionStore is attached, conversation history
// is injected before the current message and persisted after the response.
func (l *Loop) Process(ctx context.Context, sessionKey, message string) (string, error) {
	messages := l.buildMessages(sessionKey, message)

	// Track repeated identical tool failures to break infinite loops.
	failCounts := make(map[failKey]int)

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
			l.persistExchange(sessionKey, message, content)
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
		var execErr error
		messages, execErr = l.executeToolCalls(ctx, messages, resp.ToolCalls, iteration, failCounts)
		if execErr != nil {
			return "", execErr
		}

		// Warn the agent when approaching the iteration limit so it can escalate proactively.
		// Injected AFTER tool responses to preserve the required message ordering:
		// assistant(tool_calls) → tool(response) → ... → user(warning).
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

// buildMessages constructs the initial message list with system prompt, optional
// session history, and the current user message.
func (l *Loop) buildMessages(sessionKey, message string) []provider.Message {
	messages := []provider.Message{
		{Role: "system", Content: l.systemPrompt},
	}

	if l.sessions != nil && sessionKey != "" {
		if summary := l.sessions.GetSummary(sessionKey); summary != "" {
			messages = append(messages,
				provider.Message{Role: "user", Content: "[Previous conversation summary]\n" + summary},
				provider.Message{Role: "assistant", Content: "Understood, I have the context from our previous conversation."},
			)
		}
		messages = append(messages, l.sessions.Get(sessionKey)...)
	}

	messages = append(messages, provider.Message{Role: "user", Content: message})
	return messages
}

// persistExchange saves user+assistant messages to the session store.
func (l *Loop) persistExchange(sessionKey, userMsg, assistantMsg string) {
	if l.sessions == nil || sessionKey == "" {
		return
	}
	l.sessions.Add(sessionKey, provider.Message{Role: "user", Content: userMsg})
	l.sessions.Add(sessionKey, provider.Message{Role: "assistant", Content: assistantMsg})
	if err := l.sessions.Save(sessionKey); err != nil {
		slog.Warn("session save failed", slog.String("key", sessionKey), slog.Any("error", err))
	}
}

func (l *Loop) executeToolCalls(ctx context.Context, messages []provider.Message, toolCalls []provider.ToolCall, iteration int, failCounts map[failKey]int) ([]provider.Message, error) {
	for _, tc := range toolCalls {
		name, args := parseToolCall(tc)

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
				return messages, fmt.Errorf("tool %q keeps failing with the same error (%q) after %d attempts — stopping to avoid infinite loop",
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

	return messages, nil
}

func parseToolCall(tc provider.ToolCall) (name string, args map[string]any) {
	name = tc.Name
	if name == "" && tc.Function != nil {
		name = tc.Function.Name
	}

	// Parse arguments if not already parsed.
	args = tc.Args
	if args == nil && tc.Function != nil && tc.Function.Arguments != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err != nil {
			slog.Warn("failed to parse tool call arguments", slog.String("tool", name), slog.Any("error", err))
		} else {
			args = parsed
		}
	}

	return name, args
}
