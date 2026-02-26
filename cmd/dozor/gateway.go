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
	"github.com/anatolykoptev/dozor/internal/approvals"
	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/mcpclient"
	"github.com/anatolykoptev/dozor/internal/session"
	"github.com/anatolykoptev/dozor/internal/telegram"
	"github.com/anatolykoptev/dozor/internal/tools"
	mcpclientExt "github.com/anatolykoptev/dozor/pkg/extensions/mcpclient"
)

const (
	// webhookBodyLimit is the maximum bytes read from a webhook POST body.
	webhookBodyLimit = 32000
	// kbSearchTimeoutSec is the timeout for KB search during watch triage (seconds).
	kbSearchTimeoutSec = 30
	// remoteCheckTimeoutSec is the timeout for checking remote server status (seconds).
	remoteCheckTimeoutSec = 30
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
	extRegistry := buildExtensionRegistry(eng, stack.registry, mcpServer, true, notifyFn)

	// Extract KBSearcher for programmatic KB access (triage enrichment + auto-save).
	var kbSearcher *mcpclient.KBSearcher
	if ext := extRegistry.Get("mcpclient"); ext != nil {
		if mcpExt, ok := ext.(*mcpclientExt.MCPClientExtension); ok {
			kbSearcher = mcpExt.KBSearcher()
		}
	}
	if kbSearcher != nil {
		slog.Info("knowledge base integration active")
	}

	// 4. HTTP mux: MCP + health + webhook.
	mx := http.NewServeMux()
	mx.Handle("/mcp", buildMCPHTTPHandler(mcpServer))
	mx.Handle("/mcp/", buildMCPHTTPHandler(mcpServer))
	mx.HandleFunc("GET /health", healthHandler("gateway"))
	registerWebhookHandler(mx, msgBus)

	// 5. A2A protocol.
	a2aSecret := os.Getenv("DOZOR_A2A_SECRET")
	a2a.Register(mx, stack.loop, stack.registry, "http://127.0.0.1:"+port, version, a2aSecret)

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

	// 7. Interactive session manager.
	sessionMgr := session.NewManager()
	defer sessionMgr.CloseAll()
	sessionCfg := session.ConfigFromEnv()

	// 8. Message handler: inbound → agent → outbound.
	go runMessageLoop(sigCtx, messageLoopDeps{
		msgBus:       msgBus,
		stack:        stack,
		adminChatID:  adminChatID,
		approvalsMgr: approvalsMgr,
		notifyFn:     notifyFn,
		kbSearcher:   kbSearcher,
		sessionMgr:   sessionMgr,
		sessionCfg:   sessionCfg,
	})

	// 9. Periodic watch (if configured).
	if interval := os.Getenv("DOZOR_WATCH_INTERVAL"); interval != "" {
		dur, err := time.ParseDuration(interval)
		if err != nil {
			slog.Error("invalid DOZOR_WATCH_INTERVAL", slog.String("value", interval)) //nolint:gosec // interval is from own env
		} else {
			go runGatewayWatch(sigCtx, eng, msgBus, dur, cfg, kbSearcher, notifyFn)
		}
	}

	// 9b. Fast remote server checks (independent of LLM pipeline).
	if cfg.HasRemote() {
		go runRemoteWatch(sigCtx, cfg, notifyFn)
	}

	// 10. HTTP server (blocks until shutdown).
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
		body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit))
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
		slog.Info("webhook received", slog.String("path", source), slog.Int("len", len(text))) //nolint:gosec // path is safe to log

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
