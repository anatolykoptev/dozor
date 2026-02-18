package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/a2a"
	"github.com/anatolykoptev/dozor/internal/agent"
	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/skills"
	"github.com/anatolykoptev/dozor/internal/telegram"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/dozor/pkg/extensions"
	"github.com/anatolykoptev/dozor/pkg/extensions/a2aclient"
	"github.com/anatolykoptev/dozor/pkg/extensions/mcpclient"
	"github.com/anatolykoptev/dozor/pkg/extensions/websearch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	// Load .env
	loadDotenv(".env")

	cfg := engine.Init()
	eng := engine.NewAgent(cfg)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(cfg, eng)
	case "gateway":
		runGateway(cfg, eng)
	case "check":
		runCheck(cfg, eng)
	case "watch":
		runWatch(cfg, eng)
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `dozor - server monitoring agent

Usage:
  dozor serve [--port PORT] [--stdio]        MCP server (HTTP or stdio)
  dozor gateway [--port PORT]                Full agent: MCP + A2A + Telegram
  dozor check [--json] [--services s1,s2]    One-shot diagnostics
  dozor watch [--interval 4h] [--smart]      Periodic monitoring daemon
`)
}

func runServe(cfg engine.Config, eng *engine.ServerAgent) {
	stdio := hasFlag("--stdio")

	logWriter := os.Stdout
	if stdio {
		logWriter = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dozor",
		Version: version,
	}, nil)

	// Core tools
	tools.RegisterAll(server, eng)
	
	// Extension system
	extRegistry := extensions.NewRegistry()
	extRegistry.Register(websearch.New())
	extRegistry.LoadAll(context.Background(), eng, nil, server)
	extRegistry.RegisterIntrospectTool(server)
	for _, extErr := range extRegistry.Errors() {
		logger.Warn("extension error", slog.String("ext", extErr.Extension), slog.String("phase", string(extErr.Phase)), slog.Any("error", extErr.Err))
	}
	logger.Info("dozor MCP server", slog.Int("tools", 11), slog.Int("extensions", len(extRegistry.List())))

	if stdio {
		logger.Info("running in stdio mode")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			logger.Error("stdio server failed", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}

	// HTTP mode
	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mx := http.NewServeMux()
	mx.Handle("/mcp", handler)
	mx.Handle("/mcp/", handler)
	mx.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","service":"dozor","version":"%s"}`, version)
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mx,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-sigCtx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
	logger.Info("stopped")
}

// runGateway starts the full agent: MCP + A2A + Telegram + LLM.
func runGateway(cfg engine.Config, eng *engine.ServerAgent) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	port := cfg.MCPPort
	if p := getFlagValue("--port"); p != "" {
		port = p
	}

	// 1. Tool registry + bridge tools.
	registry := toolreg.NewRegistry()
	toolreg.RegisterAll(registry, eng)

	// Client tools moved to extension system

	// 1b. Workspace + Skills.
	workspacePath := os.Getenv("DOZOR_WORKSPACE")
	if workspacePath == "" {
		home, _ := os.UserHomeDir()
		workspacePath = home + "/.dozor"
	}

	// Resolve builtin skills directory (next to binary or cwd).
	builtinSkillsDir := resolveBuiltinDir("skills")
	defaultsDir := resolveBuiltinDir("workspace")
	skills.InitWorkspace(workspacePath, defaultsDir)

	skillsLoader := skills.NewLoader(workspacePath+"/skills", builtinSkillsDir)
	skills.RegisterTools(registry, skillsLoader)
	skills.RegisterMemoryTools(registry, workspacePath)

	logger.Info("tool registry initialized", slog.Int("tools", len(registry.List())),
		slog.Int("skills", len(skillsLoader.ListSkills())))

	// 2. LLM provider.
	llm := provider.NewFromEnv()

	// 3. Agent loop.
	loop := agent.NewLoop(llm, registry, llm.MaxIterations(), workspacePath, skillsLoader)

	// 4. Message bus.
	msgBus := bus.New()
	defer msgBus.Close()

	// 5. MCP server (same as serve).
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "dozor",
		Version: version,
	}, nil)
	
	// Core tools
	tools.RegisterAll(mcpServer, eng)
	
	// Extension system
	extRegistry := extensions.NewRegistry()
	extRegistry.Register(websearch.New())
	extRegistry.Register(mcpclient.New())
	extRegistry.Register(a2aclient.New())
	extRegistry.LoadAll(context.Background(), eng, registry, mcpServer)
	extRegistry.RegisterIntrospectTool(mcpServer)
	for _, extErr := range extRegistry.Errors() {
		logger.Warn("extension error", slog.String("ext", extErr.Extension), slog.String("phase", string(extErr.Phase)), slog.Any("error", extErr.Err))
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	// 6. HTTP mux.
	mx := http.NewServeMux()
	mx.Handle("/mcp", mcpHandler)
	mx.Handle("/mcp/", mcpHandler)
	mx.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","service":"dozor","version":"%s","mode":"gateway"}`, version)
	})

	// 7. A2A protocol (if secret configured).
	a2aSecret := os.Getenv("DOZOR_A2A_SECRET")
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)
	a2a.Register(mx, loop, registry, baseURL, version, a2aSecret)

	// 7a. Webhook endpoint — external monitors can send alerts to the agent.
	webhookHandler := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32000))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Accept JSON with "text" or "message" field, or plain text.
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

		source := r.URL.Path // e.g. "/webhook" or "/webhook/monitor/healthcheck"
		logger.Info("webhook received", slog.String("path", source), slog.Int("len", len(text)))

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
	mx.HandleFunc("POST /webhook", webhookHandler)
	mx.HandleFunc("POST /webhook/", webhookHandler) // catch-all for /webhook/monitor/healthcheck etc.

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 8. Telegram channel (if token configured).
	if os.Getenv("DOZOR_TELEGRAM_TOKEN") != "" {
		tg, err := telegram.New(msgBus)
		if err != nil {
			logger.Error("telegram init failed", slog.Any("error", err))
		} else {
			tg.Start(sigCtx)
			logger.Info("telegram channel active")
		}
	}

	// 9. Resolve Telegram admin chat for internal notifications.
	adminChatID := os.Getenv("DOZOR_TELEGRAM_ADMIN")
	if adminChatID == "" {
		// Default to first allowed user (chat ID = user ID for DMs).
		if ids := os.Getenv("DOZOR_TELEGRAM_ALLOWED"); ids != "" {
			adminChatID = strings.TrimSpace(strings.Split(ids, ",")[0])
		}
	}

	// 10. Message handler goroutine: bus.inbound → agent.Process → bus.outbound.
	go func() {
		for {
			msg, ok := msgBus.ConsumeInbound(sigCtx)
			if !ok {
				return
			}
			logger.Info("processing message",
				slog.String("channel", msg.Channel),
				slog.String("sender", msg.SenderID))

			response, err := loop.Process(sigCtx, msg.Text)
			if err != nil {
				logger.Error("agent processing failed", slog.Any("error", err))
				response = fmt.Sprintf("Error: %s", err.Error())
			}

			// Route response back to origin channel.
			msgBus.PublishOutbound(bus.Message{
				ID:        msg.ID + "-reply",
				Channel:   msg.Channel,
				SenderID:  "dozor",
				ChatID:    msg.ChatID,
				Text:      response,
				Timestamp: time.Now(),
			})

			// Forward internal channel responses to Telegram for visibility.
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
	}()

	// 11. Start HTTP server.
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mx,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second, // longer for LLM calls via A2A
	}

	go func() {
		logger.Info("gateway listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	// 12. Built-in watch loop — periodic triage routed through the agent.
	if interval := os.Getenv("DOZOR_WATCH_INTERVAL"); interval != "" {
		dur, err := time.ParseDuration(interval)
		if err != nil {
			logger.Error("invalid DOZOR_WATCH_INTERVAL", slog.String("value", interval))
		} else {
			go runGatewayWatch(sigCtx, eng, msgBus, dur)
		}
	}

	<-sigCtx.Done()
	logger.Info("shutting down gateway")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
	logger.Info("gateway stopped")
}

// runGatewayWatch runs periodic triage and feeds results through the message bus.
func runGatewayWatch(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus, interval time.Duration) {
	slog.Info("gateway watch started", slog.String("interval", interval.String()))

	// Initial check after short delay (let everything boot).
	time.Sleep(30 * time.Second)
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

func runCheck(cfg engine.Config, eng *engine.ServerAgent) {
	ctx := context.Background()
	asJSON := hasFlag("--json")

	// Override services if specified
	var services []string
	if s := getFlagValue("--services"); s != "" {
		services = strings.Split(s, ",")
	}

	report := eng.Diagnose(ctx, services)

	if asJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(engine.FormatReport(report))
	}

	if report.NeedsAttention() {
		os.Exit(1)
	}
}

func runWatch(cfg engine.Config, eng *engine.ServerAgent) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if i := getFlagValue("--interval"); i != "" {
		if d, err := time.ParseDuration(i); err == nil {
			cfg.WatchInterval = d
		}
	}
	if u := getFlagValue("--webhook"); u != "" {
		cfg.WebhookURL = u
	}

	smart := hasFlag("--smart")

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if smart {
		// Smart watch: triage → LLM analysis → autonomous actions.
		logger.Info("smart watch mode (LLM-enabled)")

		registry := toolreg.NewRegistry()
		toolreg.RegisterAll(registry, eng)

		workspacePath := os.Getenv("DOZOR_WORKSPACE")
		if workspacePath == "" {
			home, _ := os.UserHomeDir()
			workspacePath = home + "/.dozor"
		}
		builtinSkillsDir := resolveBuiltinDir("skills")
		skillsLoader := skills.NewLoader(workspacePath+"/skills", builtinSkillsDir)

		llm := provider.NewFromEnv()
		loop := agent.NewLoop(llm, registry, llm.MaxIterations(), workspacePath, skillsLoader)

		runSmartWatch(sigCtx, cfg, eng, loop)
		return
	}

	if err := engine.Watch(sigCtx, cfg); err != nil {
		logger.Error("watch failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func runSmartWatch(ctx context.Context, cfg engine.Config, eng *engine.ServerAgent, loop *agent.Loop) {
	var prevHash string

	slog.Info("smart watch started",
		slog.String("interval", cfg.WatchInterval.String()))

	prevHash = runSmartCheck(ctx, cfg, eng, loop, prevHash)

	ticker := time.NewTicker(cfg.WatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("smart watch stopped")
			return
		case <-ticker.C:
			prevHash = runSmartCheck(ctx, cfg, eng, loop, prevHash)
		}
	}
}

func runSmartCheck(ctx context.Context, cfg engine.Config, eng *engine.ServerAgent, loop *agent.Loop, prevHash string) string {
	slog.Info("running smart health check")

	// First, get the triage report.
	triageResult := eng.Triage(ctx, nil)
	if triageResult == "" {
		slog.Info("triage returned empty, all healthy")
		return "healthy"
	}

	// Hash the triage to detect changes.
	hash := hashString(triageResult)
	if hash == prevHash {
		slog.Info("no changes in triage results")
		return hash
	}

	slog.Info("triage state changed, analyzing with LLM")

	// Feed triage to agent loop for analysis and autonomous action.
	prompt := fmt.Sprintf(`The following is a server triage report. Analyze the issues and take corrective action if safe to do so. After taking any actions, verify the fixes.

Triage Report:
%s`, triageResult)

	response, err := loop.Process(ctx, prompt)
	if err != nil {
		slog.Error("LLM analysis failed", slog.Any("error", err))
		// Fall back to sending raw triage.
		if cfg.WebhookURL != "" {
			sendSmartWebhook(ctx, cfg.WebhookURL, "Dozor Smart Watch (LLM failed):\n\n"+triageResult)
		}
		return hash
	}

	slog.Info("LLM analysis complete", slog.Int("response_len", len(response)))

	// Send summary to webhook.
	if cfg.WebhookURL != "" {
		sendSmartWebhook(ctx, cfg.WebhookURL, "Dozor Smart Watch Report:\n\n"+response)
	}

	return hash
}

func hashString(s string) string {
	h := fmt.Sprintf("%x", []byte(s))
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

func sendSmartWebhook(ctx context.Context, url, text string) {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		slog.Error("webhook request failed", slog.Any("error", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("webhook send failed", slog.Any("error", err))
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("webhook returned error", slog.Int("status", resp.StatusCode))
	}
}

// hasFlag checks if a flag exists in os.Args.
func hasFlag(flag string) bool {
	for _, a := range os.Args[2:] {
		if a == flag {
			return true
		}
	}
	return false
}

// getFlagValue returns the value after a flag (--flag value).
func getFlagValue(flag string) string {
	args := os.Args[2:]
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		// Handle --flag=value
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

// resolveBuiltinDir finds a directory relative to the executable or cwd.
func resolveBuiltinDir(name string) string {
	// Try next to executable first.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	// Fall back to cwd.
	if cwd, err := os.Getwd(); err == nil {
		dir := filepath.Join(cwd, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// loadDotenv loads a .env file into os environment if it exists.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Don't override existing env vars
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
