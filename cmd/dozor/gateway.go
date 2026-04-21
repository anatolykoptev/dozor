package main

import (
	"context"
	"fmt"
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
	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/session"
	"github.com/anatolykoptev/dozor/internal/telegram"
	"github.com/anatolykoptev/dozor/internal/tools"
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	// 1. Agent stack: tool registry, skills, LLM provider, session store.
	// The loop itself is attached after extensions load — it needs the
	// extension-provided KBSearcher for the Phase 6.3 startup snapshot.
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

	// Extract KBSearcher for programmatic KB access (triage enrichment +
	// auto-save) and Phase 6.3 startup snapshot injection.
	kbSearcher := kbSearcherFromExtensions(extRegistry)
	if kbSearcher != nil {
		slog.Info("knowledge base integration active")
	}

	// 3b. Attach the agent loop now that we can pass the searcher for the
	// startup snapshot. stack.loop is used from step 5 onwards.
	stack.attachLoop(kbSearcher)

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 4. HTTP mux: MCP + health + webhook.
	mx := http.NewServeMux()
	mx.Handle("/mcp", buildMCPHTTPHandler(mcpServer))
	mx.Handle("/mcp/", buildMCPHTTPHandler(mcpServer))
	mx.HandleFunc("GET /health", healthHandler("gateway"))
	registerWebhookHandler(mx, msgBus, notifyFn)
	registerDeployWebhook(sigCtx, mx, notifyFn)

	// 5. A2A protocol.
	a2aSecret := os.Getenv("DOZOR_A2A_SECRET")
	a2a.Register(mx, stack.loop, stack.registry, "http://127.0.0.1:"+port, version, a2aSecret)

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

// registerDeployWebhook sets up the GitHub webhook handler for auto-rebuild.
// If the deploy config file is missing, the handler is silently skipped.
func registerDeployWebhook(ctx context.Context, mx *http.ServeMux, notifyFn func(string)) {
	cfgPath := deploy.DefaultConfigPath()
	cfg, err := deploy.LoadConfig(cfgPath)
	if err != nil {
		slog.Info("deploy webhook disabled", slog.String("reason", err.Error()))
		return
	}

	// Log deploy lifecycle to journalctl instead of Telegram — visibility for debug.
	deployLog := func(msg string) { slog.Info("deploy", "msg", msg) }
	queue := deploy.NewQueue(ctx, deployLog)
	handler := deploy.NewHandler(cfg, queue, deployLog)
	mx.Handle("POST /deploy/github", handler)

	slog.Info("deploy webhook active",
		slog.String("path", "/deploy/github"),
		slog.Int("repos", len(cfg.Repos)),
	)
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
