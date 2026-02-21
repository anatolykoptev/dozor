package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/a2a"
	"github.com/anatolykoptev/dozor/internal/approvals"
	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/telegram"
)

// runGateway starts the full agent: MCP + A2A + Telegram + LLM.
func runGateway(cfg engine.Config, eng *engine.ServerAgent) {
	defer eng.Close()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	// 1. Agent stack: tool registry, skills, LLM provider, agent loop.
	stack := buildAgentStack(eng)

	// 2. Message bus + notify (created before MCP server so exec tool can use them).
	msgBus := bus.New()
	defer msgBus.Close()

	adminChatID := resolveAdminChatID()
	approvalsMgr := approvals.New()
	notifyFn := func(text string) {
		if adminChatID == "" {
			return
		}
		msgBus.PublishOutbound(bus.Message{
			ID:        fmt.Sprintf("notify-%d", time.Now().UnixMilli()),
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    adminChatID,
			Text:      text,
			Timestamp: time.Now(),
		})
	}

	// 3. MCP server + extensions.
	execConfig := tools.NewExecConfig()
	execOpts := tools.ExecOptions{Config: execConfig, Approvals: approvalsMgr, Notify: notifyFn}
	mcpServer := buildMCPServer(eng, execOpts)
	buildExtensionRegistry(eng, stack.registry, mcpServer, true, notifyFn)

	// 4. HTTP mux: MCP + health + webhook.
	mx := http.NewServeMux()
	mx.Handle("/mcp", buildMCPHTTPHandler(mcpServer))
	mx.Handle("/mcp/", buildMCPHTTPHandler(mcpServer))
	mx.HandleFunc("GET /health", healthHandler("gateway"))
	registerWebhookHandler(mx, msgBus)

	// 5. A2A protocol.
	a2aSecret := os.Getenv("DOZOR_A2A_SECRET")
	a2a.Register(mx, stack.loop, stack.registry, fmt.Sprintf("http://127.0.0.1:%s", port), version, a2aSecret)

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 6. Telegram channel.
	if os.Getenv("DOZOR_TELEGRAM_TOKEN") != "" {
		tg, err := telegram.New(msgBus)
		if err != nil {
			slog.Error("telegram init failed", slog.Any("error", err))
		} else {
			tg.Start(sigCtx)
			slog.Info("telegram channel active")
		}
	}

	// 7. Message handler: inbound → agent → outbound.
	go runMessageLoop(sigCtx, msgBus, stack, adminChatID, approvalsMgr, notifyFn)

	// 8. Periodic watch (if configured).
	if interval := os.Getenv("DOZOR_WATCH_INTERVAL"); interval != "" {
		dur, err := time.ParseDuration(interval)
		if err != nil {
			slog.Error("invalid DOZOR_WATCH_INTERVAL", slog.String("value", interval))
		} else {
			go runGatewayWatch(sigCtx, eng, msgBus, dur)
		}
	}

	// 9. HTTP server (blocks until shutdown).
	startHTTPServer(sigCtx, &http.Server{
		Addr:         ":" + port,
		Handler:      mx,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second,
	}, "gateway")
}

// registerWebhookHandler adds POST /webhook and POST /webhook/ to the mux.
func registerWebhookHandler(mx *http.ServeMux, msgBus *bus.Bus) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32000))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var payload struct {
			Text    string `json:"text"`
			Message string `json:"message"`
		}
		text := string(body)
		if json.Unmarshal(body, &payload) == nil {
			if payload.Text != "" {
				text = payload.Text
			} else if payload.Message != "" {
				text = payload.Message
			}
		}

		source := r.URL.Path
		slog.Info("webhook received", slog.String("path", source), slog.Int("len", len(text)))

		msgBus.PublishInbound(bus.Message{
			ID:        fmt.Sprintf("webhook-%d", time.Now().UnixMilli()),
			Channel:   "internal",
			SenderID:  "webhook",
			ChatID:    "webhook",
			Text:      "ALERT from external monitor (" + source + "):\n\n" + text,
			Timestamp: time.Now(),
		})

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"accepted"}`)
	}

	mx.HandleFunc("POST /webhook", handler)
	mx.HandleFunc("POST /webhook/", handler)
}

// resolveAdminChatID returns the Telegram admin chat ID for internal notifications.
func resolveAdminChatID() string {
	if id := os.Getenv("DOZOR_TELEGRAM_ADMIN"); id != "" {
		return id
	}
	if ids := os.Getenv("DOZOR_TELEGRAM_ALLOWED"); ids != "" {
		return strings.TrimSpace(strings.Split(ids, ",")[0])
	}
	return ""
}

// runMessageLoop processes inbound messages through the agent loop and routes responses.
func runMessageLoop(ctx context.Context, msgBus *bus.Bus, stack *agentStack, adminChatID string, approvalsMgr *approvals.Manager, notifyFn func(string)) {
	for {
		msg, ok := msgBus.ConsumeInbound(ctx)
		if !ok {
			return
		}
		slog.Info("processing message",
			slog.String("channel", msg.Channel),
			slog.String("sender", msg.SenderID))

		// Check if this is a command approval response ("yes exec-XXXXXXXX" / "no exec-XXXXXXXX").
		if approvalsMgr != nil {
			if id, approved, ok := approvals.ParseResponse(msg.Text); ok {
				if approvalsMgr.Resolve(id, approved) {
					verdict := "✅ Команда одобрена"
					if !approved {
						verdict = "❌ Команда отклонена"
					}
					msgBus.PublishOutbound(bus.Message{
						ID:        msg.ID + "-approval-ack",
						Channel:   "telegram",
						SenderID:  "dozor",
						ChatID:    msg.ChatID,
						Text:      verdict,
						Timestamp: time.Now(),
					})
					continue
				}
			}
		}

		if msg.Channel == "telegram" && msg.ChatID != "" {
			msgBus.PublishOutbound(bus.Message{
				ID:        msg.ID + "-ack",
				Channel:   "telegram",
				SenderID:  "dozor",
				ChatID:    msg.ChatID,
				Text:      "⏳ Принял, обрабатываю",
				Timestamp: time.Now(),
			})
		}

		response, err := stack.loop.Process(ctx, msg.Text)
		if err != nil {
			slog.Error("agent processing failed", slog.Any("error", err))
			if strings.Contains(err.Error(), "max tool iterations reached") {
				response = "⚠️ Превышен лимит итераций. Передаю задачу Claude Code для глубокого анализа..."
				go autoEscalateToClaudeCode(ctx, stack, msg.Text, notifyFn)
			} else {
				response = fmt.Sprintf("Error: %s", err.Error())
			}
		}

		msgBus.PublishOutbound(bus.Message{
			ID:        msg.ID + "-reply",
			Channel:   msg.Channel,
			SenderID:  "dozor",
			ChatID:    msg.ChatID,
			Text:      response,
			Timestamp: time.Now(),
		})

		if msg.Channel == "internal" && adminChatID != "" {
			msgBus.PublishOutbound(bus.Message{
				ID:        msg.ID + "-tg-notify",
				Channel:   "telegram",
				SenderID:  "dozor",
				ChatID:    adminChatID,
				Text:      response,
				Timestamp: time.Now(),
			})
		}
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
		"## Задача\n%s\n\n"+
		"## Что произошло\n"+
		"Агент Dozor выполнял задачу и исчерпал лимит инструментальных итераций.\n"+
		"Задача не была завершена. Требуется глубокий анализ и исправление.\n\n"+
		"## Последние логи Dozor\n%s\n\n"+
		"## Инструкции\n"+
		"1. Проанализируй логи и пойми, на чём застрял агент\n"+
		"2. Определи корневую причину проблемы\n"+
		"3. Реши проблему или предложи конкретный план действий\n"+
		"4. Если нужны права или команды — выполни их",
		originalTask, logSnippet)

	slog.Info("escalating to claude_code after max iterations")
	shortTask := originalTask
	if len(shortTask) > 100 {
		shortTask = shortTask[:100] + "..."
	}
	result, execErr := stack.registry.Execute(ctx, "claude_code", map[string]any{
		"prompt": prompt,
		"async":  true,
		"title":  "⚠️ Авто-эскалация: превышен лимит итераций\n" + shortTask,
	})
	if execErr != nil {
		slog.Error("claude_code escalation failed", slog.Any("error", execErr))
		if notify != nil {
			notify(fmt.Sprintf("❌ Claude Code escalation failed: %s", execErr.Error()))
		}
		return
	}
	slog.Info("claude_code escalation result", slog.String("result", result))
}

// runGatewayWatch runs periodic triage and feeds results through the message bus.
func runGatewayWatch(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus, interval time.Duration) {
	slog.Info("gateway watch started", slog.String("interval", interval.String()))
	time.Sleep(30 * time.Second) // let everything boot
	gatewayWatchTick(ctx, eng, msgBus)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("gateway watch stopped")
			return
		case <-ticker.C:
			gatewayWatchTick(ctx, eng, msgBus)
		}
	}
}

func gatewayWatchTick(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus) {
	slog.Info("gateway watch: running triage", slog.Bool("dev_mode", eng.IsDevMode()))
	result := eng.Triage(ctx, nil)
	if result == "" {
		slog.Info("gateway watch: all healthy")
		return
	}

	var prompt string
	if eng.IsDevMode() {
		prompt = "Periodic health check (DEV MODE — observe only, do NOT take any corrective action):\n\n"
	} else {
		prompt = "Periodic health check detected issues. First use memdb_search to check for similar past incidents and proven solutions, then analyze and take corrective action if safe:\n\n"
	}

	slog.Info("gateway watch: issues detected, routing to agent")
	msgBus.PublishInbound(bus.Message{
		ID:        fmt.Sprintf("watch-%d", time.Now().UnixMilli()),
		Channel:   "internal",
		SenderID:  "watch",
		ChatID:    "watch",
		Text:      prompt + result,
		Timestamp: time.Now(),
	})
}
