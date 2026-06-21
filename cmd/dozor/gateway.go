package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/a2a"
	"github.com/anatolykoptev/dozor/internal/approvals"
	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/quotas"
	"github.com/anatolykoptev/dozor/internal/session"
	"github.com/anatolykoptev/dozor/internal/telegram"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/go-kit/tracing"
	"github.com/anatolykoptev/go-kit/tracing/httpmw"
	"github.com/anatolykoptev/go-kit/tracing/slogh"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// webhookBodyLimit is the maximum bytes read from a webhook POST body.
	webhookBodyLimit = 32000
	// kbSearchTimeoutSec is the timeout for KB search during watch triage (seconds).
	kbSearchTimeoutSec = 30
	// remoteCheckTimeoutSec is the timeout for checking remote server status (seconds).
	remoteCheckTimeoutSec = 30
)

// renderMu serializes calls to engine.RenderAlertCard.
//
// satori-render is a single-threaded Node process. Concurrent render requests
// pile up and the slowest exceed DOZOR_SATORI_TIMEOUT, triggering text-fallback.
// Even after the batch fix (one notifyAlertFn call per webhook POST), independent
// alert sources (alertmanager + remote-watch) can still race. Mutex caps inflight
// renders at 1 within this process, keeping each request well inside the timeout.
var renderMu sync.Mutex

// runGateway starts the full agent: MCP + A2A + Telegram + LLM.
func runGateway(cfg engine.Config, eng *engine.ServerAgent) {
	defer eng.Close()
	slog.SetDefault(slog.New(slogh.NewHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))))

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

	// notifyAlertFn delivers a structured alert as a Telegram photo card,
	// rendered via the satori-render sidecar (no LLM in the path). On any
	// rendering failure (sidecar down, satori error, network) it falls back
	// to the plain-text notifyFn so the operator never loses the alert.
	// The textual content (caption when the card succeeds, full message on
	// fallback) is the same FormatRemoteAlerts string the previous flow used.
	notifyAlertFn := func(alerts []engine.Alert, fallbackText string) {
		if adminChatID == "" {
			return
		}
		if len(alerts) == 0 {
			notifyFn(fallbackText)
			return
		}
		// Capture into the active-alert ring so the alerts-active MCP tool can
		// surface these otherwise fire-and-forget alerts after Telegram delivery.
		// We record before rendering so that even a satori render failure does
		// not cause the alert to be omitted from the ring.
		for i := range alerts {
			engine.DefaultAlertRing.Add(alerts[i])
		}
		renderMu.Lock()
		card, err := engine.RenderAlertCard(context.Background(), alerts[0])
		renderMu.Unlock()
		if err != nil {
			slog.Warn("alert card render failed, falling back to text",
				slog.Any("error", err),
				slog.String("service", alerts[0].Service))
			notifyFn(fallbackText)
			return
		}
		caption := fallbackText
		if len(caption) > 1024 {
			caption = caption[:1021] + "..."
		}
		msgBus.PublishOutbound(bus.Message{
			ID:        fmt.Sprintf("alert-%d", time.Now().UnixMilli()),
			Channel:   "telegram",
			SenderID:  "dozor",
			ChatID:    adminChatID,
			Text:      caption,
			Photo:     card,
			Timestamp: time.Now(),
		})
		slog.Info("alert card sent",
			slog.String("service", alerts[0].Service),
			slog.String("level", string(alerts[0].Level)),
			slog.Int("png_bytes", len(card)))
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

	// OTel tracing. If OTEL_EXPORTER_OTLP_ENDPOINT is unset, Setup returns a
	// no-op shutdown and Start emits no-op spans — context still propagates so
	// trace_id stays stable across services if upstream sends it.
	traceShutdown, err := tracing.Setup(sigCtx, "dozor", tracing.WithSampleRatio(1.0))
	if err != nil {
		slog.Warn("tracing setup failed", slog.Any("error", err))
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = traceShutdown(shutdownCtx)
	}()
	defer webhookLimiter.Close()

	// 4. HTTP mux: MCP + health + webhook + metrics.
	mx := httpmw.NewServeMux()
	mx.Handle("/mcp", buildMCPHTTPHandler(mcpServer))
	mx.Handle("/mcp/", buildMCPHTTPHandler(mcpServer))
	mx.HandleFunc("GET /health", healthHandler("gateway"))
	mx.Handle("/metrics", promhttp.Handler())
	registerWebhookHandler(mx.ServeMux, msgBus, notifyFn, notifyAlertFn)
	registerAlertmanagerWebhookHandler(mx.ServeMux, notifyAlertFn)
	registerDeployWebhook(sigCtx, mx.ServeMux, notifyFn, notifyAlertFn)
	if dockerCli := eng.DockerClient(); dockerCli != nil {
		registerLogsHandler(mx.ServeMux, dockerCli)
	} else {
		slog.Warn("/api/logs not registered: docker client unavailable (Docker unreachable at startup)")
	}

	// 5. A2A protocol (fail-closed: exits if DOZOR_A2A_SECRET is unset and
	// DOZOR_A2A_ALLOW_INSECURE is not explicitly set to "true").
	a2aSecret := os.Getenv("DOZOR_A2A_SECRET")
	if err := a2a.Register(mx.ServeMux, stack.loop, stack.registry, "http://127.0.0.1:"+port, version, a2aSecret); err != nil {
		slog.Error("a2a registration failed — refusing to start", slog.Any("error", err))
		os.Exit(1)
	}

	// 6. Telegram channel.
	// Bind durable TG message log before telegram.Start so the very first sends
	// are persisted. Flush is deferred for graceful-shutdown persistence.
	engine.DefaultTGLog.BindPersistence(engine.DefaultTGLogPath())
	defer func() {
		if err := engine.DefaultTGLog.Flush(); err != nil {
			slog.Warn("tg-message-log: shutdown flush failed", slog.Any("error", err))
		}
	}()

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
		go runRemoteWatch(sigCtx, cfg, notifyFn, notifyAlertFn)
	}

	// 9c. Vendor quota probes.
	go runQuotasWatch(sigCtx, quotas.LoadConfigFrom(cfg.LLMCfg), notifyFn)

	// 10. HTTP server (blocks until shutdown).
	bindHost := resolveBindHost()
	slog.Info("binding gateway", slog.String("addr", bindHost+":"+port))
	if !isLoopbackBind(bindHost) {
		slog.Warn("gateway bound to non-loopback interface — network-reachable",
			slog.String("bind_host", bindHost),
			slog.String("hint", "set DOZOR_BIND_HOST=127.0.0.1 for loopback-only binding"))
	}
	startHTTPServer(sigCtx, &http.Server{
		Addr:         bindHost + ":" + port,
		Handler:      httpmw.Handler("dozor", recoveryMiddleware(mx)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second,
	}, "gateway")
}

// registerDeployWebhook sets up the GitHub webhook handler for auto-rebuild.
// If the deploy config file is missing, the handler is silently skipped.
func registerDeployWebhook(ctx context.Context, mx *http.ServeMux, notifyFn func(string), notifyAlertFn func([]engine.Alert, string)) {
	cfgPath := deploy.DefaultConfigPath()
	cfg, err := deploy.LoadConfig(cfgPath)
	if err != nil {
		slog.Info("deploy webhook disabled", slog.String("reason", err.Error()))
		return
	}

	// Log all deploy lifecycle events to journalctl.
	//
	// DOZOR_DEPLOY_NOTIFY controls which deploy events reach Telegram:
	//   "failure"  (default) — only ❌/⚠️ failure and rollback messages.
	//   "all"                — also ✅ success and 🔨 build-start messages
	//                          (restores original verbose behaviour).
	//
	// With auto-deploy on every push a busy day produces 20+ ✅ pings; the
	// default keeps the channel quiet on routine deploys.
	deployNotifyMode := strings.ToLower(strings.TrimSpace(os.Getenv("DOZOR_DEPLOY_NOTIFY")))
	if deployNotifyMode == "" {
		deployNotifyMode = "failure"
	}
	slog.Info("deploy notify mode", slog.String("mode", deployNotifyMode))
	deployLog := makeDeployLog(deployNotifyMode, notifyFn, notifyAlertFn)
	// DOZOR_BUILD_CONCURRENCY controls how many Docker builds may run
	// concurrently across all service groups. Default 1 preserves the
	// original serialized behaviour; set to 2 once the ARM host has been
	// validated under concurrent load. Heavy builds (heavy: true in
	// deploy-repos.yaml) always have an additional serialization constraint
	// (heavySem) regardless of this knob.
	buildConcurrency := 1
	if v := os.Getenv("DOZOR_BUILD_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			buildConcurrency = n
		} else {
			slog.Warn("DOZOR_BUILD_CONCURRENCY: invalid value, using 1",
				slog.String("value", v))
		}
	}
	slog.Info("deploy queue starting",
		slog.Int("concurrency", buildConcurrency))
	queue := deploy.NewQueueN(ctx, deployLog, buildConcurrency)
	// Durable build queue: mirror pending + in-flight builds to
	// ~/.dozor/deploy-queue.json so they survive a dozor restart (graceful
	// self-deploy / crash / OOM). See queue_persist.go (VOLATILE-PENDING-STATE
	// fix, queue layer — downstream of the debounce fix).
	queue.WithPersistence(deploy.DefaultQueuePersistPath())
	handler := deploy.NewHandler(cfg, queue, deployLog)
	// Recover survivors from a previous dozor process. ORDER MATTERS:
	//  1. RecoverQueue re-enqueues queued + interrupted-in-flight builds, which
	//     repopulates the queue's pending set, THEN
	//  2. RecoverPending replays debounce entries — both route through
	//     Queue.Submit, which dedups by (service-key, SHA), so a commit recovered
	//     by BOTH layers produces exactly one build (no double-recovery).
	if err := queue.RecoverQueue(ctx); err != nil {
		slog.Warn("deploy: queue recovery failed", "error", err)
	}
	handler.RecoverPending(ctx)
	mx.Handle("POST /deploy/github", handler)

	// Tear down debouncer goroutines when the gateway shuts down.
	go func() {
		<-ctx.Done()
		handler.Close()
	}()

	slog.Info("deploy webhook active",
		slog.String("path", "/deploy/github"),
		slog.Int("repos", len(cfg.Repos)),
	)
}

// makeDeployLog returns a deploy lifecycle callback that logs every event to
// journalctl and, based on mode, forwards a filtered subset to Telegram. It
// also bumps tgNotificationsTotal so suppression is observable.
//
// Deploy FAILURES (❌ error, ⚠️ rollback) render as deterministic alert cards
// via notifyAlertFn so they are visually identical to every other service-ops
// alert. Routine ✅ success / 🔨 build-start messages stay plain text (cheap —
// the operator cares about failure visibility, not routine success chatter).
//
// mode values (DOZOR_DEPLOY_NOTIFY):
//
//	"failure" (default) — only ❌/⚠️ failures + rollbacks reach TG (as cards).
//	"all"               — also ✅ success and 🔨 build-start (as plain text).
func makeDeployLog(mode string, notifyFn func(string), notifyAlertFn func([]engine.Alert, string)) func(string) {
	return func(msg string) {
		slog.Info("deploy", "msg", msg)
		isError := strings.HasPrefix(msg, "❌")
		isRollback := strings.HasPrefix(msg, "⚠️")
		isFailure := isError || isRollback
		isSuccess := strings.HasPrefix(msg, "✅")

		notifyFailureCard := func() {
			level := engine.AlertCritical
			if isRollback {
				level = engine.AlertWarning
			}
			notifyAlertFn([]engine.Alert{{
				Level:       level,
				Service:     "deploy",
				Title:       deployAlertTitle(msg),
				Description: msg,
				Timestamp:   time.Now(),
			}}, msg)
		}

		switch mode {
		case "all":
			if isFailure {
				notifyFailureCard()
				tgNotificationsTotal.WithLabelValues("deploy_failure", "sent").Inc()
			} else {
				notifyFn(msg)
				if isSuccess {
					tgNotificationsTotal.WithLabelValues("deploy_success", "sent").Inc()
				}
			}
		default: // "failure"
			if isFailure {
				notifyFailureCard()
				tgNotificationsTotal.WithLabelValues("deploy_failure", "sent").Inc()
			} else if isSuccess {
				tgNotificationsTotal.WithLabelValues("deploy_success", "suppressed").Inc()
			}
		}
	}
}

// deployAlertTitle strips the leading emoji marker and returns the first line of
// a deploy message for use as the alert-card title (the full message stays in
// the card description / text fallback).
func deployAlertTitle(msg string) string {
	title := strings.TrimSpace(strings.TrimLeft(msg, "❌⚠️✅🔨 "))
	if nl := strings.IndexByte(title, '\n'); nl >= 0 {
		title = title[:nl]
	}
	if title == "" {
		return "deploy event"
	}
	return title
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
