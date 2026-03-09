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

// watchDeps groups dependencies for the gateway watch loop.
type watchDeps struct {
	eng        *engine.ServerAgent
	msgBus     *bus.Bus
	cfg        engine.Config
	kbSearcher *mcpclient.KBSearcher
	notify     func(string)
	lastHash   string
}

// runGatewayWatch runs periodic triage and feeds results through the message bus.
func runGatewayWatch(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus, interval time.Duration, cfg engine.Config, kbSearcher *mcpclient.KBSearcher, notify func(string)) {
	slog.Info("gateway watch started", slog.String("interval", interval.String())) //nolint:gosec // derived from duration
	time.Sleep(30 * time.Second) // let everything boot

	w := &watchDeps{
		eng:        eng,
		msgBus:     msgBus,
		cfg:        cfg,
		kbSearcher: kbSearcher,
		notify:     notify,
	}
	w.tick(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("gateway watch stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *watchDeps) tick(ctx context.Context) {
	slog.Info("gateway watch: running triage", slog.Bool("dev_mode", w.eng.IsDevMode()))

	result := w.collectReport(ctx)
	if w.isHealthy(result) {
		slog.Info("gateway watch: all healthy")
		w.lastHash = ""
		return
	}

	hash := hashResult(result)
	if hash == w.lastHash {
		slog.Info("gateway watch: same issues, suppressed (dedup)", slog.String("hash", hash))
		return
	}
	w.lastHash = hash

	if !w.eng.IsDevMode() && tryAutoRemediate(ctx, w.eng, w.cfg, result, w.notify) {
		return
	}

	w.routeToAgent(ctx, result, hash)
}

// collectReport runs triage, systemd checks, and extra alerts into a single report.
func (w *watchDeps) collectReport(ctx context.Context) string {
	result := w.eng.Triage(ctx, nil)

	if alerts := checkSystemdServices(ctx, w.eng); alerts != "" {
		result += "\n\n" + alerts
	}

	result += collectExtraAlerts(ctx, w.cfg)
	return result
}

// isHealthy returns true when the report contains no actionable issues.
func (w *watchDeps) isHealthy(result string) bool {
	return strings.Contains(result, "\nHealth: healthy |") &&
		!strings.Contains(result, "[CRITICAL]") &&
		!strings.Contains(result, "[ERROR]") &&
		!strings.Contains(result, "[WARNING]")
}

// routeToAgent logs detected issues and sends the triage to the LLM agent.
func (w *watchDeps) routeToAgent(ctx context.Context, result, hash string) {
	issues := engine.ExtractIssues(result)
	for _, issue := range issues {
		slog.Warn("gateway watch: issue detected",
			slog.String("service", issue.Service),
			slog.String("description", issue.Description),
		)
	}

	result = stripHealthyLine(result)
	prompt := buildWatchPrompt(w.eng.IsDevMode())
	prompt += enrichWithKB(ctx, w.kbSearcher, result)

	slog.Info("gateway watch: routing to agent",
		slog.String("hash", hash),
		slog.Int("issues", len(issues)),
	)
	w.msgBus.PublishInbound(bus.Message{
		ID:        fmt.Sprintf("watch-%d", time.Now().UnixMilli()),
		Channel:   "internal",
		SenderID:  "watch",
		ChatID:    "watch",
		Text:      prompt + result,
		Timestamp: time.Now(),
	})
}

func hashResult(result string) string {
	h := sha256.Sum256([]byte(result))
	return hex.EncodeToString(h[:8])
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

	query := buildKBQuery(issues)

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

// buildKBQuery joins issue descriptions into a truncated search query.
func buildKBQuery(issues []engine.TriageIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Description)
	}
	query := strings.Join(parts, "; ")
	if len(query) > kbQueryMaxLen {
		query = query[:kbQueryMaxLen]
	}
	return query
}

// checkSystemdServices discovers user systemd services and reports any that are not active.
func checkSystemdServices(ctx context.Context, eng *engine.ServerAgent) string {
	services := eng.ResolveUserServices(ctx)
	if len(services) == 0 {
		return ""
	}

	names := engine.UserServiceNamesFrom(services)
	status := eng.GetSystemdStatus(ctx, names)

	var down []string
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "[!!] ") {
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
