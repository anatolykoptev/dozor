package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anatolykoptev/dozor/internal/agent"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/provider"
	"github.com/anatolykoptev/dozor/internal/skills"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// runWatch starts the periodic monitoring daemon (simple or smart/LLM mode).
func runWatch(cfg engine.Config, eng *engine.ServerAgent) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if i := getFlagValue("--interval"); i != "" {
		if d, err := time.ParseDuration(i); err == nil {
			cfg.WatchInterval = d
		}
	}
	if u := getFlagValue("--webhook"); u != "" {
		cfg.WebhookURL = u
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if hasFlag("--smart") {
		slog.Info("smart watch mode (LLM-enabled)")
		loop := buildSmartWatchLoop(eng)
		runSmartWatch(sigCtx, cfg, eng, loop)
		return
	}

	if err := engine.Watch(sigCtx, cfg); err != nil {
		slog.Error("watch failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// buildSmartWatchLoop creates the minimal agent stack needed for smart watch.
// Unlike buildAgentStack, this skips skill memory tools and workspace init.
func buildSmartWatchLoop(eng *engine.ServerAgent) *agent.Loop {
	workspacePath := resolveWorkspacePath()
	builtinSkillsDir := resolveBuiltinDir("skills")

	registry := toolreg.NewRegistry()
	toolreg.RegisterAll(registry, eng)

	skillsLoader := skills.NewLoader(workspacePath+"/skills", builtinSkillsDir)

	llm := provider.NewFromEnv()
	return agent.NewLoop(llm, registry, llm.MaxIterations(), workspacePath, skillsLoader)
}

// runSmartWatch periodically runs triage and feeds issues through the LLM agent.
func runSmartWatch(ctx context.Context, cfg engine.Config, eng *engine.ServerAgent, loop *agent.Loop) {
	slog.Info("smart watch started", slog.String("interval", cfg.WatchInterval.String()))

	var prevHash string
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

// runSmartCheck runs a single triage cycle and feeds changes to the LLM agent.
func runSmartCheck(ctx context.Context, cfg engine.Config, eng *engine.ServerAgent, loop *agent.Loop, prevHash string) string {
	slog.Info("running smart health check")

	triageResult := eng.Triage(ctx, nil)
	if triageResult == "" {
		slog.Info("triage returned empty, all healthy")
		return "healthy"
	}

	hash := hashString(triageResult)
	if hash == prevHash {
		slog.Info("no changes in triage results")
		return hash
	}

	slog.Info("triage state changed, analyzing with LLM")

	prompt := fmt.Sprintf(`The following is a server triage report. Analyze the issues and take corrective action if safe to do so. After taking any actions, verify the fixes.

Triage Report:
%s`, triageResult)

	response, err := loop.Process(ctx, prompt)
	if err != nil {
		slog.Error("LLM analysis failed", slog.Any("error", err))
		if cfg.WebhookURL != "" {
			sendWebhook(ctx, cfg.WebhookURL, "Dozor Smart Watch (LLM failed):\n\n"+triageResult)
		}
		return hash
	}

	slog.Info("LLM analysis complete", slog.Int("response_len", len(response)))

	if cfg.WebhookURL != "" {
		sendWebhook(ctx, cfg.WebhookURL, "Dozor Smart Watch Report:\n\n"+response)
	}

	return hash
}
