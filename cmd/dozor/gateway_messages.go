package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/approvals"
	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/mcpclient"
	"github.com/anatolykoptev/dozor/internal/session"
)

// messageLoopDeps groups dependencies for the message loop to reduce parameter count.
type messageLoopDeps struct {
	msgBus       *bus.Bus
	stack        *agentStack
	adminChatID  string
	approvalsMgr *approvals.Manager
	notifyFn     func(string)
	kbSearcher   *mcpclient.KBSearcher
	sessionMgr   *session.Manager
	sessionCfg   session.Config
}

// runMessageLoop processes inbound messages through the agent loop and routes responses.
func runMessageLoop(ctx context.Context, deps messageLoopDeps) {
	for {
		msg, ok := deps.msgBus.ConsumeInbound(ctx)
		if !ok {
			return
		}
		slog.Info("processing message",
			slog.String("channel", msg.Channel),
			slog.String("sender", msg.SenderID))

		if handleApproval(deps.approvalsMgr, deps.msgBus, msg) {
			continue
		}

		if routeToSession(ctx, deps.msgBus, deps.sessionMgr, deps.sessionCfg, msg) {
			continue
		}

		processAgentMessage(ctx, deps, msg)
	}
}

// handleApproval checks if a message is a command approval response and resolves it.
// Returns true if the message was an approval and was handled.
func handleApproval(mgr *approvals.Manager, msgBus *bus.Bus, msg bus.Message) bool {
	if mgr == nil {
		return false
	}
	id, approved, ok := approvals.ParseResponse(msg.Text)
	if !ok {
		return false
	}
	if !mgr.Resolve(id, approved) {
		return false
	}
	verdict := "✅ Command approved"
	if !approved {
		verdict = "❌ Command rejected"
	}
	msgBus.PublishOutbound(bus.Message{
		ID:        msg.ID + "-approval-ack",
		Channel:   "telegram",
		SenderID:  "dozor",
		ChatID:    msg.ChatID,
		Text:      verdict,
		Timestamp: time.Now(),
	})
	return true
}

// processAgentMessage sends a message through the agent loop and publishes the response.
func processAgentMessage(ctx context.Context, deps messageLoopDeps, msg bus.Message) {
	if msg.Channel == "telegram" && msg.ChatID != "" {
		deps.msgBus.PublishOutbound(bus.Message{
			ID:        msg.ID + "-ack",
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    msg.ChatID,
			Text:      "⏳ Processing...",
			Timestamp: time.Now(),
		})
	}

	response, err := deps.stack.loop.Process(ctx, msg.Text)
	if err != nil {
		slog.Error("agent processing failed", slog.Any("error", err))
		if strings.Contains(err.Error(), "max tool iterations reached") {
			response = "⚠️ Max iterations reached. Escalating to Claude Code for deep analysis..."
			go autoEscalateToClaudeCode(ctx, deps.stack, msg.Text, deps.notifyFn)
		} else {
			response = "Error: " + err.Error()
		}
	}

	deps.msgBus.PublishOutbound(bus.Message{
		ID:        msg.ID + "-reply",
		Channel:   msg.Channel,
		SenderID:  "dozor",
		ChatID:    msg.ChatID,
		Text:      response,
		Timestamp: time.Now(),
	})

	if msg.Channel == "internal" && deps.adminChatID != "" {
		deps.msgBus.PublishOutbound(bus.Message{
			ID:        msg.ID + "-tg-notify",
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    deps.adminChatID,
			Text:      response,
			Timestamp: time.Now(),
		})
	}
}

// autoEscalateToClaudeCode collects recent logs and delegates the task to Claude Code.
// Called in a goroutine when the agent loop hits max iterations.
func autoEscalateToClaudeCode(ctx context.Context, stack *agentStack, originalTask string, notify func(string)) {
	// Collect last 60 lines of dozor logs for context.
	out, err := exec.CommandContext(ctx,
		"journalctl", "--user", "-u", "dozor", "-n", "60", "--no-pager", "--output=short").Output()
	logSnippet := string(out)
	if err != nil || len(logSnippet) == 0 {
		logSnippet = "(logs unavailable)"
	}

	prompt := fmt.Sprintf(
		"## Task\n%s\n\n"+
			"## What happened\n"+
			"Dozor agent was executing a task and exhausted the tool iteration limit.\n"+
			"The task was not completed. Deep analysis and resolution required.\n\n"+
			"## Recent Dozor logs\n%s\n\n"+
			"## Instructions\n"+
			"1. Analyze the logs and identify where the agent got stuck\n"+
			"2. Determine the root cause\n"+
			"3. Fix the problem or propose a concrete action plan\n"+
			"4. Execute any necessary commands",
		originalTask, logSnippet)

	slog.Info("escalating to claude_code after max iterations")
	shortTask := originalTask
	if len(shortTask) > 100 {
		shortTask = shortTask[:100] + "..."
	}
	result, execErr := stack.registry.Execute(ctx, "claude_code", map[string]any{
		"prompt": prompt,
		"async":  true,
		"title":  "⚠️ Auto-escalation: max iterations exceeded\n" + shortTask,
	})
	if execErr != nil {
		slog.Error("claude_code escalation failed", slog.Any("error", execErr))
		if notify != nil {
			notify("❌ Claude Code escalation failed: " + execErr.Error())
		}
		return
	}
	slog.Info("claude_code escalation result", slog.String("result", result))
}

// routeToSession handles session-related commands for telegram messages.
// Returns true if the message was handled by a session and should not be processed further.
func routeToSession(ctx context.Context, msgBus *bus.Bus, mgr *session.Manager, cfg session.Config, msg bus.Message) bool {
	if msg.Channel != "telegram" || msg.ChatID == "" {
		return false
	}

	text := strings.TrimSpace(msg.Text)

	if strings.HasPrefix(text, "/claude") {
		prompt := strings.TrimSpace(strings.TrimPrefix(text, "/claude"))
		go handleStartSession(ctx, msgBus, mgr, cfg, msg, prompt)
		return true
	}
	if text == "/dozor" {
		handleExitSession(msgBus, mgr, msg)
		return true
	}
	if sess := mgr.Get(msg.ChatID); sess != nil {
		sess.Send(text)
		return true
	}
	return false
}

// handleStartSession starts a new interactive Claude Code session.
// Runs in a goroutine to avoid blocking the message loop.
func handleStartSession(ctx context.Context, msgBus *bus.Bus, mgr *session.Manager, cfg session.Config, msg bus.Message, prompt string) {
	if prompt == "" {
		prompt = "Hello! I'm ready to help. What would you like to work on?"
	}

	// Acknowledge.
	msgBus.PublishOutbound(bus.Message{
		ID:        msg.ID + "-session-ack",
		Channel:   "telegram",
		SenderID:  "dozor",
		ChatID:    msg.ChatID,
		Text:      "🚀 Starting Claude Code session...",
		Timestamp: time.Now(),
	})

	notifyFn := func(chatID, text string) {
		msgBus.PublishOutbound(bus.Message{
			ID:        fmt.Sprintf("session-%s-%d", chatID, time.Now().UnixMilli()),
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    chatID,
			Text:      text,
			Timestamp: time.Now(),
		})
	}

	sess, err := session.Start(ctx, msg.ChatID, prompt, cfg, notifyFn)
	if err != nil {
		slog.Error("session start failed", slog.String("chat_id", msg.ChatID), slog.Any("error", err))
		msgBus.PublishOutbound(bus.Message{
			ID:        msg.ID + "-session-err",
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    msg.ChatID,
			Text:      "❌ Failed to start session: " + err.Error(),
			Timestamp: time.Now(),
		})
		return
	}

	mgr.Set(msg.ChatID, sess)
	slog.Info("session started", slog.String("chat_id", msg.ChatID))
}

// handleExitSession closes the active Claude Code session and returns to dozor agent.
func handleExitSession(msgBus *bus.Bus, mgr *session.Manager, msg bus.Message) {
	if mgr.Get(msg.ChatID) == nil {
		msgBus.PublishOutbound(bus.Message{
			ID:        msg.ID + "-no-session",
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    msg.ChatID,
			Text:      "Dozor agent active (no Claude Code session to close).",
			Timestamp: time.Now(),
		})
		return
	}

	mgr.Delete(msg.ChatID)
	msgBus.PublishOutbound(bus.Message{
		ID:        msg.ID + "-session-exit",
		Channel:   "telegram",
		SenderID:  "dozor",
		ChatID:    msg.ChatID,
		Text:      "✅ Claude Code session closed. Dozor agent active.",
		Timestamp: time.Now(),
	})
	slog.Info("session ended by user", slog.String("chat_id", msg.ChatID))
}
