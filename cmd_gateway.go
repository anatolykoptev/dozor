package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/a2a"
	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/telegram"
)

// runGateway starts the full agent: MCP + A2A + Telegram + LLM.
func runGateway(cfg engine.Config, eng *engine.ServerAgent) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	// 1. Agent stack: tool registry, skills, LLM provider, agent loop.
	stack := buildAgentStack(eng)

	// 2. MCP server + extensions.
	mcpServer := buildMCPServer(eng)
	buildExtensionRegistry(eng, stack.registry, mcpServer, true)

	// 3. Message bus.
	msgBus := bus.New()
	defer msgBus.Close()

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
	adminChatID := resolveAdminChatID()
	go runMessageLoop(sigCtx, msgBus, stack, adminChatID)

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
func runMessageLoop(ctx context.Context, msgBus *bus.Bus, stack *agentStack, adminChatID string) {
	for {
		msg, ok := msgBus.ConsumeInbound(ctx)
		if !ok {
			return
		}
		slog.Info("processing message",
			slog.String("channel", msg.Channel),
			slog.String("sender", msg.SenderID))

		response, err := stack.loop.Process(ctx, msg.Text)
		if err != nil {
			slog.Error("agent processing failed", slog.Any("error", err))
			response = fmt.Sprintf("Error: %s", err.Error())
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
	slog.Info("gateway watch: running triage")
	result := eng.Triage(ctx, nil)
	if result == "" {
		slog.Info("gateway watch: all healthy")
		return
	}
	slog.Info("gateway watch: issues detected, routing to agent")
	msgBus.PublishInbound(bus.Message{
		ID:        fmt.Sprintf("watch-%d", time.Now().UnixMilli()),
		Channel:   "internal",
		SenderID:  "watch",
		ChatID:    "watch",
		Text:      "Periodic health check detected issues. Analyze and take corrective action if safe:\n\n" + result,
		Timestamp: time.Now(),
	})
}
