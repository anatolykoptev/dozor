package a2a

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
)

// MessageProcessor processes a text message and returns a text response.
type MessageProcessor interface {
	Process(ctx context.Context, message string) (string, error)
}

// Executor implements a2asrv.AgentExecutor by bridging to the agent loop.
type Executor struct {
	proc MessageProcessor
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// NewExecutor creates a new A2A executor.
func NewExecutor(proc MessageProcessor) *Executor {
	return &Executor{proc: proc}
}

// Execute processes an incoming A2A message through the agent loop.
func (e *Executor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	content := extractText(reqCtx.Message)
	if content == "" {
		event := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateFailed,
			a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx, a2a.TextPart{Text: "empty message"}))
		event.Final = true
		return queue.Write(ctx, event)
	}

	// Signal working state.
	workingEvent := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateWorking, nil)
	if err := queue.Write(ctx, workingEvent); err != nil {
		return fmt.Errorf("write working status: %w", err)
	}

	// Process through agent loop (no streaming for now).
	response, err := e.proc.Process(ctx, content)
	if err != nil {
		failEvent := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateFailed,
			a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx, a2a.TextPart{Text: err.Error()}))
		failEvent.Final = true
		return queue.Write(ctx, failEvent)
	}

	// Send completed status.
	completedEvent := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateCompleted,
		a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx, a2a.TextPart{Text: response}))
	completedEvent.Final = true
	return queue.Write(ctx, completedEvent)
}

// Cancel writes a canceled status event.
func (e *Executor) Cancel(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	event := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateCanceled, nil)
	event.Final = true
	return queue.Write(ctx, event)
}

func extractText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var parts []string
	for _, p := range msg.Parts {
		if tp, ok := p.(a2a.TextPart); ok {
			parts = append(parts, tp.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
