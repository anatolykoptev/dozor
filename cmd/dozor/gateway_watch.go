package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/mcpclient"
	kitenv "github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/toolutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// watchReportTotal counts mechanical watch reports sent to the operator,
// by severity. The LLM route this replaced failed silently (6/6 HTTP 413
// visible only in logs) — this counter makes the alert path assertable.
var watchReportTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "dozor_watch_report_total",
	Help: "Mechanical watch reports sent to the operator, by severity.",
}, []string{"severity"})

// tgNotificationsTotal counts proactive Telegram notifications by kind and
// action (sent|suppressed). "Proactive" means operator-unprompted — deploy
// status, watch alerts, recovery messages. Reply-path (operator-command
// responses) is excluded.
//
// kind values:
//
//	"watch_issue"     — new issue set detected by periodic triage
//	"watch_recovery"  — service set recovered to healthy
//	"deploy_success"  — build finished successfully (only when DOZOR_DEPLOY_NOTIFY=all)
//	"deploy_failure"  — build failed or rolled back
//
// action values:  "sent" | "suppressed"
//
// Example PromQL to see daily suppression rate for watch:
//
//	rate(dozor_tg_notifications_total{kind=~"watch.*",action="suppressed"}[24h])
//	  / rate(dozor_tg_notifications_total{kind=~"watch.*"}[24h])
var tgNotificationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "dozor_tg_notifications_total",
	Help: "Proactive Telegram notifications by kind and action (sent|suppressed).",
}, []string{"kind", "action"})

const (
	// kbQueryMaxLen is the maximum length of KB search query built from issues.
	kbQueryMaxLen = 500
	// kbTopKResults is the number of KB results to fetch during watch triage.
	kbTopKResults = 3
	// mechReportMaxIssues caps the number of issue lines in a mechanical
	// report so a mass outage stays within one Telegram message.
	mechReportMaxIssues = 12
	// llmCheckEveryDefault gates the LLM key canary to every Nth watch
	// tick: 6 ticks at the 5-min interval = one canary per 30 min.
	llmCheckEveryDefault = 6
)

// watchDeps groups dependencies for the gateway watch loop.
type watchDeps struct {
	eng        *engine.ServerAgent
	msgBus     *bus.Bus
	cfg        engine.Config
	kbSearcher *mcpclient.KBSearcher
	notify     func(string)
	// lastHash is the hash of the last triage result (healthy or not).
	// Reset to "" on recovery so the next unhealthy tick computes a fresh hash.
	lastHash string
	// lastNotifiedHash is the hash that was most recently notified to the
	// operator (via routeFn + TG). It outlives a healthy interval so that
	// when the same issue set returns after a brief recovery, the
	// notifyCooldown can suppress it even if lastHash was cleared.
	// Reset only when a recovery notification is sent.
	lastNotifiedHash string
	// wasUnhealthy tracks whether the previous tick was unhealthy so we can
	// detect the unhealthy→healthy state transition and send a recovery notice.
	wasUnhealthy   bool
	notifyCooldown *notifyCooldown
	// tickNum counts watch ticks (1-based); llmCheckEvery gates the LLM
	// key canary to every Nth tick (DOZOR_LLM_CHECK_EVERY, default 6 —
	// 30 min at the 5-min watch interval). cachedLLMAlerts replays the
	// last canary result on gated-off ticks.
	tickNum         int
	llmCheckEvery   int
	cachedLLMAlerts string
	// routeFn is called to dispatch a triage result that auto-remediation
	// did not fully handle. Defaults to w.mechanicalReport (deterministic
	// Telegram report, no LLM); DOZOR_WATCH_LLM=true restores the legacy
	// LLM agent route. Overridable in tests.
	routeFn func(ctx context.Context, result, hash string)
}

// runGatewayWatch runs periodic triage and feeds results through the message bus.
func runGatewayWatch(ctx context.Context, eng *engine.ServerAgent, msgBus *bus.Bus, interval time.Duration, cfg engine.Config, kbSearcher *mcpclient.KBSearcher, notify func(string)) {
	defer toolutil.RecoverLog("runGatewayWatch")

	slog.Info("gateway watch started", slog.String("interval", interval.String())) //nolint:gosec // derived from duration
	time.Sleep(30 * time.Second)                                                   // let everything boot

	w := &watchDeps{
		eng:            eng,
		msgBus:         msgBus,
		cfg:            cfg,
		kbSearcher:     kbSearcher,
		notify:         notify,
		notifyCooldown: newNotifyCooldownFromEnv(),
		llmCheckEvery:  kitenv.Int("DOZOR_LLM_CHECK_EVERY", llmCheckEveryDefault),
	}
	w.routeFn = w.mechanicalReport
	if kitenv.Bool("DOZOR_WATCH_LLM", false) {
		slog.Info("gateway watch: LLM route enabled (DOZOR_WATCH_LLM)")
		w.routeFn = w.defaultRouteToAgent
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
	w.tickNum++
	slog.Debug("gateway watch: running triage", slog.Bool("dev_mode", w.eng.IsDevMode()))

	result := w.collectReport(ctx)
	if w.isHealthy(result) {
		w.noteHealthy()
		return
	}

	// Mark that this tick is unhealthy so the next healthy tick can detect
	// the unhealthy→healthy state transition.
	w.wasUnhealthy = true

	hash := hashResult(result)
	if hash == w.lastHash {
		tgNotificationsTotal.WithLabelValues("watch_issue", "suppressed").Inc()
		slog.Info("gateway watch: same issues, suppressed (dedup)", slog.String("hash", hash))
		return
	}
	w.lastHash = hash

	if !w.eng.IsDevMode() && tryAutoRemediate(ctx, w.eng, w.cfg, result, w.notify) {
		return
	}

	now := time.Now()
	// Suppress if the same hash was notified within the cooldown window,
	// even when lastHash was cleared by an intervening healthy tick.
	if w.notifyCooldown.shouldSuppress(hash, now) {
		tgNotificationsTotal.WithLabelValues("watch_issue", "suppressed").Inc()
		slog.InfoContext(ctx, "watch: notify cooldown active, suppressing watch report",
			"hash", hash, "cooldown", w.notifyCooldown.duration)
		return
	}
	w.routeFn(ctx, result, hash)
	w.notifyCooldown.markSent(hash, now) // mark AFTER successful route
	w.lastNotifiedHash = hash
	tgNotificationsTotal.WithLabelValues("watch_issue", "sent").Inc()
}

// noteHealthy records a healthy tick. On the unhealthy→healthy state
// transition (wasUnhealthy == true) it sends a recovery TG notice and
// resets state so the same issue set can fire again after it returns.
func (w *watchDeps) noteHealthy() {
	if w.wasUnhealthy {
		// State transition: unhealthy → healthy.
		slog.Info("gateway watch: recovered", slog.String("last_hash", w.lastNotifiedHash))
		if w.notify != nil && w.lastNotifiedHash != "" {
			w.notify("✅ <b>Dozor Watch</b> — all services recovered")
			tgNotificationsTotal.WithLabelValues("watch_recovery", "sent").Inc()
		}
		// Clear lastNotifiedHash so the same issue set can re-fire after
		// recovery. The notifyCooldown still suppresses rapid flapping
		// within the cooldown window.
		w.lastNotifiedHash = ""
		w.wasUnhealthy = false
	} else {
		slog.Debug("gateway watch: all healthy")
	}
	w.lastHash = ""
}

// collectReport runs triage, systemd checks, and extra alerts into a single report.
func (w *watchDeps) collectReport(ctx context.Context) string {
	result := w.eng.Triage(ctx, nil)

	if alerts := checkSystemdServices(ctx, w.eng); alerts != "" {
		result += "\n\n" + alerts
	}

	result += collectExtraAlerts(ctx, w.cfg)
	result += w.llmKeyAlerts(ctx)
	return result
}

// isHealthy returns true when the report contains no actionable issues.
func (w *watchDeps) isHealthy(result string) bool {
	return strings.Contains(result, "\nHealth: healthy |") &&
		!strings.Contains(result, "[CRITICAL]") &&
		!strings.Contains(result, "[ERROR]") &&
		!strings.Contains(result, "[WARNING]")
}

// defaultRouteToAgent logs detected issues and sends the triage to the LLM agent.
// Assigned to routeFn by default; overridable in tests via the routeFn field.
func (w *watchDeps) defaultRouteToAgent(ctx context.Context, result, hash string) {
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

	var issueNames []string
	for _, issue := range issues {
		issueNames = append(issueNames, issue.Service+": "+issue.Description)
	}
	slog.Info("gateway watch: routing to agent",
		slog.String("hash", hash),
		slog.Int("issues", len(issues)),
		slog.String("summary", strings.Join(issueNames, "; ")))
	w.msgBus.PublishInbound(bus.Message{
		ID:        fmt.Sprintf("watch-%d", time.Now().UnixMilli()),
		Channel:   "internal",
		SenderID:  "watch",
		ChatID:    "watch",
		Text:      prompt + result,
		Timestamp: time.Now(),
	})
}

// mechanicalReport formats unhandled triage issues into a deterministic
// Telegram HTML report and delivers it via notify — no LLM in the path.
// Default watch route: the LLM step added no signal over the structured
// triage data and its ~24K-token prompt (system prompt + KB context +
// full report) overflowed provider TPM budgets on every incident
// (groq 12K TPM → HTTP 413, 6/6 failures over 48 h).
func (w *watchDeps) mechanicalReport(_ context.Context, result, hash string) {
	issues := engine.ExtractIssues(result)
	for _, issue := range issues {
		slog.Warn("gateway watch: issue detected",
			slog.String("service", issue.Service),
			slog.String("description", issue.Description),
		)
	}

	severity := reportSeverity(issues)
	report := buildMechanicalReport(issues, hash, time.Now())
	watchReportTotal.WithLabelValues(severity).Inc()
	slog.Info("gateway watch: mechanical report sent",
		slog.String("hash", hash),
		slog.String("severity", severity),
		slog.Int("issues", len(issues)))
	if w.notify != nil {
		w.notify(report)
	}
}

// buildMechanicalReport renders the Telegram HTML body for unhandled issues.
// Mirrors the shape the LLM route was prompted to produce (Status / Issues /
// Action) so operator-facing alerts look the same either way.
// The header carries the send time and the dedup hash as <code>#id</code> —
// the same id slog writes with "mechanical report sent", so a report seen in
// Telegram can be located in journald (and vice versa) by one grep.
func buildMechanicalReport(issues []engine.TriageIssue, hash string, ts time.Time) string {
	var b strings.Builder
	b.WriteString("<b>Dozor Watch</b>")
	if hash != "" {
		fmt.Fprintf(&b, " <code>#%s</code>", hash)
	}
	fmt.Fprintf(&b, " — %s\n", ts.Format("2006-01-02 15:04:05 MST"))
	b.WriteString("<b>Status:</b> ")
	b.WriteString(reportSeverity(issues))

	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		lines = append(lines, fmt.Sprintf("<code>%s</code> — %s",
			html.EscapeString(issue.Service), html.EscapeString(issue.Description)))
	}

	fmt.Fprintf(&b, "\n<b>Issues (%d):</b>\n", len(lines))
	shown := lines
	if len(shown) > mechReportMaxIssues {
		shown = shown[:mechReportMaxIssues]
	}
	for _, line := range shown {
		fmt.Fprintf(&b, "• %s\n", line)
	}
	if hidden := len(lines) - len(shown); hidden > 0 {
		fmt.Fprintf(&b, "… and %d more\n", hidden)
	}

	b.WriteString("<b>Action:</b> auto-remediation did not cover these — manual check needed")
	return b.String()
}

// reportSeverity maps the highest issue level to an operator-facing status
// word. It ranks over the parsed []TriageIssue (one ranking table) instead of
// string-scanning the report, so LLM and remote alerts — now first-class issues
// in the same canonical format — rank uniformly alongside docker triage lines.
func reportSeverity(issues []engine.TriageIssue) string {
	rank := func(l engine.AlertLevel) int {
		switch l {
		case engine.AlertCritical:
			return 4
		case engine.AlertError:
			return 3
		case engine.AlertWarningHigh:
			return 2
		case engine.AlertWarning:
			return 1
		default:
			return 0
		}
	}
	var top engine.AlertLevel = engine.AlertWarning
	for _, iss := range issues {
		if rank(iss.Level) > rank(top) {
			top = iss.Level
		}
	}
	switch top {
	case engine.AlertCritical:
		return "critical"
	case engine.AlertError:
		return "degraded"
	case engine.AlertWarningHigh:
		return "warning_high"
	default:
		return "warning"
	}
}

// hashResult creates a stable hash from issue service names only,
// ignoring timestamps, error counts and log snippets that change every tick.
// This prevents repeated alerts for the same set of problematic services.
// Service names are sorted before hashing so that Docker container iteration
// order (non-deterministic) does not produce different hashes for the same
// issue set — which was bypassing cooldown suppression in production.
func hashResult(result string) string {
	issues := engine.ExtractIssues(result)
	var parts []string
	for _, issue := range issues {
		parts = append(parts, issue.Service)
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:8])
}

// collectExtraAlerts gathers alerts from the remote server check and renders
// them as canonical issue lines (engine.AlertIssueLine) so they are first-class
// to ExtractIssues — visible to dedup, severity ranking, and remediation
// routing. The rich emoji-formatted engine.FormatRemoteAlerts is a separate,
// operator-facing Telegram notification (remoteCheckTick); it is intentionally
// NOT reused here, where the machine format is required.
func collectExtraAlerts(ctx context.Context, cfg engine.Config) string {
	if !cfg.HasRemote() {
		return ""
	}

	remoteCtx, cancel := context.WithTimeout(ctx, remoteCheckTimeoutSec*time.Second)
	remoteStatus := engine.CheckRemoteServer(remoteCtx, cfg)
	cancel()
	if remoteStatus == nil || len(remoteStatus.Alerts) == 0 {
		return ""
	}

	var b strings.Builder
	for _, a := range remoteStatus.Alerts {
		b.WriteString(engine.AlertIssueLine(a))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\n" + b.String()
}

// llmKeyAlerts runs the LLM key/proxy canary, gated to every llmCheckEvery-th
// tick. Ungated it fired every 5-min tick (288 requests/day against the
// check-model's free-tier quota) for a signal that does not need 5-min
// freshness. Between gated runs the last result is replayed so the overall
// report (and its health/dedup state) does not flap with the gate.
func (w *watchDeps) llmKeyAlerts(ctx context.Context) string {
	if !w.cfg.HasLLMKeys() {
		return ""
	}
	if !shouldRunLLMCheck(w.tickNum, w.llmCheckEvery) {
		return w.cachedLLMAlerts
	}

	var text string
	if llmAlerts := engine.CheckLLMKeys(ctx, w.cfg); len(llmAlerts) > 0 {
		if t := engine.FormatLLMAlerts(llmAlerts); t != "" {
			text = "\n\n" + t
		}
	}
	w.cachedLLMAlerts = text
	return text
}

// shouldRunLLMCheck reports whether the LLM canary runs on this tick.
// Ticks are 1-based; the check always runs on the first tick after boot.
func shouldRunLLMCheck(tickNum, every int) bool {
	return every <= 1 || (tickNum-1)%every == 0
}

// buildWatchPrompt returns the system prompt prefix for a watch triage message.
func buildWatchPrompt(devMode bool) string {
	if devMode {
		return "Periodic health check (DEV MODE — observe only, do NOT take any corrective action):\n\n"
	}
	return "Health check found issues. Reply with a SHORT Telegram report (max 10 lines).\n" +
		"Use Telegram HTML formatting EXACTLY as shown — keep <b>...</b> tags literal in your reply:\n\n" +
		"<b>Status:</b> degraded/warning/critical\n" +
		"<b>Issues:</b>\n• service — problem (one line each)\n" +
		"<b>Action:</b> what you did or recommend\n\n" +
		"Allowed tags: <b>, <i>, <code>, <pre>. No other HTML. No Markdown (no **, no __).\n" +
		"IMPORTANT: Only report issues from the CURRENT triage data below. " +
		"Do NOT report numbers, restart counts, or error details from historical KB entries. " +
		"Do NOT list healthy services. Do NOT run extra diagnostics unless a service is down.\n" +
		"ACCURACY RULES — fight generic vocabulary:\n" +
		"• Quote concrete signal verbatim (HTTP code, error string, schema name, key prefix). " +
		"E.g. \"key AIza...JgIs HTTP 403 IP restriction\", NOT \"authentication errors\".\n" +
		"• Do NOT rephrase alert.Title or alert.SuggestedAction into vaguer category labels " +
		"(\"credential issues\", \"configuration errors\", \"DevOps escalation\"). " +
		"If alert says \"Google API upstream UNAVAILABLE (HTTP 503)\", report it that way.\n" +
		"• Action: state one specific verb + object. \"escalating to DevOps\" is BANNED filler.\n\n"
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
		// Self-contained line for the KB similarity query — Description no
		// longer embeds the service (see ExtractIssues).
		parts = append(parts, issue.Service+": "+issue.Description)
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
		b.WriteString(engine.FormatIssueLine(engine.AlertCritical, name, "not active"))
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
