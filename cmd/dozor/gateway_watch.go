package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/mcpclient"
)

const (
	// kbQueryMaxLen is the maximum length of KB search query built from issues.
	kbQueryMaxLen = 500
	// kbTopKResults is the number of KB results to fetch during watch triage.
	kbTopKResults = 3
)

// runGatewayWatch runs periodic triage and feeds results through the message bus.
func runGatewayWatch(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus, interval time.Duration, cfg engine.Config, kbSearcher *mcpclient.KBSearcher, notify func(string)) {
	slog.Info("gateway watch started", slog.String("interval", interval.String())) //nolint:gosec // derived from duration
	time.Sleep(30 * time.Second) // let everything boot

	var lastWatchHash string
	gatewayWatchTick(ctx, eng, msgBus, cfg, kbSearcher, &lastWatchHash, notify)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("gateway watch stopped")
			return
		case <-ticker.C:
			gatewayWatchTick(ctx, eng, msgBus, cfg, kbSearcher, &lastWatchHash, notify)
		}
	}
}

func gatewayWatchTick(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus, cfg engine.Config, kbSearcher *mcpclient.KBSearcher, lastHash *string, notify func(string)) {
	slog.Info("gateway watch: running triage", slog.Bool("dev_mode", eng.IsDevMode()))
	result := eng.Triage(ctx, nil)

	if systemdAlerts := checkSystemdServices(ctx, eng); systemdAlerts != "" {
		result += "\n\n" + systemdAlerts
	}

	triageLen := len(result)
	result += collectExtraAlerts(ctx, cfg)

	triageHealthy := strings.Contains(result[:triageLen], "\nHealth: healthy |")
	if triageHealthy && len(result) == triageLen {
		slog.Info("gateway watch: all healthy")
		*lastHash = ""
		return
	}

	h := sha256.Sum256([]byte(result))
	hash := hex.EncodeToString(h[:8])
	if hash == *lastHash {
		slog.Info("gateway watch: same issues, suppressed (dedup)", slog.String("hash", hash))
		return
	}
	*lastHash = hash

	if !eng.IsDevMode() && tryAutoRemediate(ctx, eng, cfg, result, notify) {
		return
	}

	result = stripHealthyLine(result)
	prompt := buildWatchPrompt(eng.IsDevMode())
	prompt += enrichWithKB(ctx, kbSearcher, result)

	slog.Info("gateway watch: issues detected, routing to agent", slog.String("hash", hash))
	msgBus.PublishInbound(bus.Message{
		ID:        fmt.Sprintf("watch-%d", time.Now().UnixMilli()),
		Channel:   "internal",
		SenderID:  "watch",
		ChatID:    "watch",
		Text:      prompt + result,
		Timestamp: time.Now(),
	})
}

// collectExtraAlerts gathers alerts from remote server and LLM health checks.
func collectExtraAlerts(ctx context.Context, cfg engine.Config) string {
	var extra string

	if cfg.HasRemote() {
		remoteCtx, cancel := context.WithTimeout(ctx, remoteCheckTimeoutSec*time.Second)
		remoteStatus := engine.CheckRemoteServer(remoteCtx, cfg)
		cancel()
		if remoteStatus != nil && len(remoteStatus.Alerts) > 0 {
			if text := engine.FormatRemoteAlerts(remoteStatus); text != "" {
				extra += "\n\n" + text
			}
		}
	}

	if cfg.HasLLMKeys() {
		llmAlerts := engine.CheckLLMKeys(ctx, cfg)
		if len(llmAlerts) > 0 {
			if text := engine.FormatLLMAlerts(llmAlerts); text != "" {
				extra += "\n\n" + text
			}
		}
	}

	return extra
}

// buildWatchPrompt returns the system prompt prefix for a watch triage message.
func buildWatchPrompt(devMode bool) string {
	if devMode {
		return "Periodic health check (DEV MODE — observe only, do NOT take any corrective action):\n\n"
	}
	return "Health check found issues. Reply with a SHORT Telegram report (max 10 lines):\n\n" +
		"**Status:** degraded/warning/critical\n" +
		"**Issues:**\n• service — problem (one line each)\n" +
		"**Action:** what you did or recommend\n\n" +
		"IMPORTANT: Only report issues from the CURRENT triage data below. " +
		"Do NOT report numbers, restart counts, or error details from historical KB entries. " +
		"Do NOT list healthy services. Do NOT run extra diagnostics unless a service is down.\n\n"
}

// enrichWithKB searches the knowledge base for similar past incidents and returns context to prepend.
func enrichWithKB(ctx context.Context, kbSearcher *mcpclient.KBSearcher, result string) string {
	if kbSearcher == nil {
		return ""
	}
	issues := engine.ExtractIssues(result)
	if len(issues) == 0 {
		return ""
	}

	var queries []string
	for _, issue := range issues {
		queries = append(queries, issue.Description)
	}
	query := strings.Join(queries, "; ")
	if len(query) > kbQueryMaxLen {
		query = query[:kbQueryMaxLen]
	}

	searchCtx, cancel := context.WithTimeout(ctx, kbSearchTimeoutSec*time.Second)
	kbResult, err := kbSearcher.Search(searchCtx, query, kbTopKResults)
	cancel()

	if err != nil {
		slog.Warn("kb search during triage failed", slog.Any("error", err))
		return ""
	}
	if strings.Contains(kbResult, "No relevant knowledge found") {
		return ""
	}

	slog.Info("gateway watch: enriched triage with KB context", slog.Int("issues", len(issues)))
	return "HISTORICAL CONTEXT (past resolved incidents — for reference ONLY, do NOT report these as current problems):\n" + kbResult + "\n\n"
}

// checkSystemdServices discovers user systemd services and reports any that are not active.
func checkSystemdServices(ctx context.Context, eng *engine.ServerAgent) string {
	services := eng.ResolveUserServices(ctx)
	if len(services) == 0 {
		return ""
	}

	// Batch check: get all statuses via single GetSystemdStatus call.
	names := engine.UserServiceNamesFrom(services)
	status := eng.GetSystemdStatus(ctx, names)

	// Parse "!!" markers from formatted output.
	var down []string
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "[!!] ") {
			// Format: "[!!] name: state"
			rest := strings.TrimPrefix(line, "[!!] ")
			if idx := strings.Index(rest, ":"); idx > 0 {
				down = append(down, strings.TrimSpace(rest[:idx]))
			}
		}
	}
	if len(down) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Systemd services DOWN (%d):\n", len(down))
	for _, name := range down {
		fmt.Fprintf(&b, "[CRITICAL] %s — not active\n", name)
	}
	return b.String()
}

// stripHealthyLine removes the "Healthy services (N): ..." line from triage output.
func stripHealthyLine(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "Healthy services (") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
