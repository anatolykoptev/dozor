package a2a

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
)

// MessageProcessor processes a text message and returns a text response.
type MessageProcessor interface {
	Process(ctx context.Context, sessionKey, message string) (string, error)
}

// defaultA2AMaxConcurrent caps in-flight A2A agent runs.
// Each run pins a messages slice (~1 MB) + LLM goroutines for the full HTTP
// timeout. Incident 2026-05-12: 40 concurrent vaelor a2a_call → 6.3 GB RSS.
// Override via DOZOR_A2A_MAX_CONCURRENT.
const defaultA2AMaxConcurrent = 4

// Executor implements a2asrv.AgentExecutor by bridging to the agent loop.
type Executor struct {
	proc MessageProcessor
	sem  chan struct{}
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// NewExecutor creates a new A2A executor.
func NewExecutor(proc MessageProcessor) *Executor {
	maxCon := defaultA2AMaxConcurrent
	if v := strings.TrimSpace(os.Getenv("DOZOR_A2A_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxCon = n
		} else {
			slog.Warn("a2a: invalid DOZOR_A2A_MAX_CONCURRENT, using default",
				"value", v, "default", defaultA2AMaxConcurrent)
		}
	}
	ExecutorCap.Set(float64(maxCon))
	return &Executor{
		proc: proc,
		sem:  make(chan struct{}, maxCon),
	}
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

	// Bounded concurrency — reject burst rather than pin GB of RAM.
	select {
	case e.sem <- struct{}{}:
		ExecutorInflight.Inc()
	default:
		ExecutorRejected.Inc()
		slog.Warn("a2a: concurrency cap reached, rejecting",
			"cap", cap(e.sem), "inflight", len(e.sem))
		event := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateFailed,
			a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx,
				a2a.TextPart{Text: "dozor busy: A2A concurrent cap reached, retry later"}))
		event.Final = true
		return queue.Write(ctx, event)
	}
	defer func() {
		<-e.sem
		ExecutorInflight.Dec()
	}()

	// Signal working state.
	workingEvent := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateWorking, nil)
	if err := queue.Write(ctx, workingEvent); err != nil {
		return fmt.Errorf("write working status: %w", err)
	}

	// Process through agent loop (no streaming for now).
	response, err := e.proc.Process(ctx, "a2a", content)
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
