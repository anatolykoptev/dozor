package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// diskCooldown is the package-level cooldown tracker for disk auto-remediation.
// Initialized once at startup from DOZOR_REMEDIATE_COOLDOWN (default 30m).
var diskCooldown = newRemediateCooldownFromEnv()

const (
	// postRestartVerifyDelay is how long to wait after restart before re-checking service status.
	postRestartVerifyDelay = 10 * time.Second

	// minFreedToNotifyMB is the minimum total MB freed to send a Telegram notification.
	// Below this threshold (and with no errors) the cleanup is considered a no-op and
	// the operator is not bothered. 50 MB covers rounding noise and already-empty caches.
	minFreedToNotifyMB = 50.0
)

// tryAutoRemediate attempts to handle all triage issues without LLM involvement.
// It restarts exited/dead/restarting containers, suppresses known benign warnings,
// and returns true only if ALL issues were handled. Unhandled issues fall through to LLM.
// Benign service warnings are read from cfg.SuppressWarnings (configured via DOZOR_SUPPRESS_WARNINGS).
func tryAutoRemediate(ctx context.Context, eng *engine.ServerAgent, cfg engine.Config, triageResult string, notify func(string)) bool {
	issues := engine.ExtractIssues(triageResult)
	if len(issues) == 0 {
		return false
	}

	var restarted, suppressed, remediatedDisks []string
	var unhandled []engine.TriageIssue

	for _, issue := range issues {
		level := extractIssueLevel(triageResult, issue.Service)
		r, s, d, u := routeIssue(ctx, eng, cfg, issue, level)
		restarted = append(restarted, r...)
		suppressed = append(suppressed, s...)
		remediatedDisks = append(remediatedDisks, d...)
		unhandled = append(unhandled, u...)
	}

	verified, failedRestarts := verifyRestarts(ctx, eng, restarted)
	unhandled = append(unhandled, failedRestarts...)

	if len(unhandled) > 0 {
		logUnhandled(unhandled, verified, suppressed)
		return false
	}

	if len(verified) > 0 || len(remediatedDisks) > 0 {
		msg := buildAutoRemediateMessage(verified, suppressed, remediatedDisks)
		if notify != nil {
			notify(msg)
		}
	}
	if len(verified)+len(suppressed)+len(remediatedDisks) > 0 {
		slog.Info("gateway watch: auto-remediated all issues",
			slog.Int("restarted", len(verified)),
			slog.Int("suppressed", len(suppressed)),
			slog.Int("disk_cleanups", len(remediatedDisks)))
	}

	return true
}

// routeIssue dispatches a single triage issue to the appropriate handler.
// Returns (restarted, suppressed, diskMsgs, unhandled) slices for the caller to accumulate.
func routeIssue(ctx context.Context, eng *engine.ServerAgent, cfg engine.Config, issue engine.TriageIssue, level string) (restarted, suppressed, diskMsgs []string, unhandled []engine.TriageIssue) {
	switch {
	case issue.Service == "disk":
		msg, handled := handleDiskIssue(ctx, eng, issue, level)
		if msg != "" {
			diskMsgs = append(diskMsgs, msg)
		}
		if !handled {
			unhandled = append(unhandled, issue)
		}

	case level == "CRITICAL":
		result := eng.RestartService(ctx, issue.Service)
		if result.Success {
			restarted = append(restarted, issue.Service)
		} else {
			unhandled = append(unhandled, issue)
		}

	default:
		if reason, ok := cfg.SuppressWarnings[issue.Service]; ok {
			suppressed = append(suppressed, issue.Service+" ("+reason+")")
			slog.Info("gateway watch: suppressed benign warning",
				slog.String("service", issue.Service),
				slog.String("reason", reason))
		} else {
			unhandled = append(unhandled, issue)
		}
	}
	return
}

// logUnhandled logs unhandled issues and the partial remediation summary.
func logUnhandled(unhandled []engine.TriageIssue, verified, suppressed []string) {
	for _, u := range unhandled {
		slog.Warn("gateway watch: unhandled issue",
			slog.String("service", u.Service),
			slog.String("description", u.Description),
		)
	}
	slog.Info("gateway watch: auto-remediation partial",
		slog.Int("restarted", len(verified)),
		slog.Int("suppressed", len(suppressed)),
		slog.Int("unhandled", len(unhandled)))
}

// diskRemediator is satisfied by *engine.ServerAgent and by test stubs.
type diskRemediator interface {
	AutoRemediateDisk(ctx context.Context, level engine.AlertLevel) *engine.DiskRemediateResult
}

// handleDiskIssue runs disk auto-remediation for a single disk triage issue.
// Returns (notifyMsg, handled): notifyMsg is non-empty when the operator should be notified,
// handled is true when the issue was handled (including partial runs with errors — the operator
// MUST see error notifications). Suppressed results (freed < threshold, no errors) count as
// handled but produce no notification.
// The package-level diskCooldown prevents re-running cleanup on the same (service, level)
// within the cooldown window (default 30m, tunable via DOZOR_REMEDIATE_COOLDOWN).
func handleDiskIssue(ctx context.Context, eng *engine.ServerAgent, issue engine.TriageIssue, level string) (notifyMsg string, handled bool) {
	return handleDiskIssueWithCooldown(ctx, eng, issue, level, diskCooldown, time.Now())
}

// handleDiskIssueWith is the testable core without cooldown, kept for backward compat
// with existing tests that test the raw remediation logic independently.
func handleDiskIssueWith(ctx context.Context, rem diskRemediator, issue engine.TriageIssue, level string) (notifyMsg string, handled bool) {
	alertLevel := mapTriageLevelToAlertLevel(level)
	res := rem.AutoRemediateDisk(ctx, alertLevel)
	if res == nil {
		return "", false
	}
	slog.DebugContext(ctx, "disk auto-remediate result",
		slog.Any("targets", res.Targets),
		slog.String("docker", res.Docker),
	)
	if len(res.Errors) > 0 {
		slog.WarnContext(ctx, "disk auto-remediate partial",
			slog.String("service", issue.Service),
			slog.Any("errors", res.Errors),
			slog.Any("targets", res.Targets),
		)
		// Return true (handled) so the issue does NOT land in unhandled[] — the error
		// information is surfaced via the notify message, not by falling through to LLM.
		msg := formatDiskRemediation(issue, res)
		if len(res.Errors) > 0 {
			msg += "\n[ERRORS] " + strings.Join(res.Errors, "; ")
		}
		return msg, true
	}
	if !diskRemediateShouldNotify(res) {
		slog.InfoContext(ctx, "auto-remediate ran but freed nothing meaningful, suppressing notify",
			slog.Float64("freed_mb", sumDiskFreedMB(res)),
			slog.Int("targets_attempted", len(res.Targets)),
		)
		return "", true
	}
	return formatDiskRemediation(issue, res), true
}

// verifyRestarts waits after restart and re-checks service status.
// Returns verified (running) services and issues for services that failed to recover.
func verifyRestarts(ctx context.Context, eng *engine.ServerAgent, restarted []string) (verified []string, failed []engine.TriageIssue) {
	if len(restarted) == 0 {
		return nil, nil
	}
	time.Sleep(postRestartVerifyDelay)
	for _, svc := range restarted {
		status := eng.GetServiceStatus(ctx, svc)
		if status.State == engine.StateRunning {
			verified = append(verified, svc)
		} else {
			failed = append(failed, engine.TriageIssue{
				Service:     svc,
				Description: svc + ": restart failed, still " + string(status.State),
			})
		}
	}
	return verified, failed
}

// extractIssueLevel finds the alert level prefix for a service in the triage result text.
// Matches exactly "[LEVEL] service — ..." to avoid prefix collisions (e.g. go-hully vs go-hully-worker).
func extractIssueLevel(triageResult, service string) string {
	for _, line := range strings.Split(triageResult, "\n") {
		line = strings.TrimSpace(line)
		for _, level := range []string{"CRITICAL", "ERROR", "WARNING_HIGH", "WARNING"} {
			prefix := "[" + level + "] " + service + " "
			if strings.HasPrefix(line, prefix) {
				return level
			}
		}
	}
	return ""
}

// buildAutoRemediateMessage formats a Telegram notification for auto-remediation results.
func buildAutoRemediateMessage(restarted, suppressed, disks []string) string {
	var b strings.Builder
	b.WriteString("<b>Auto-fix applied</b>\n")

	if len(restarted) > 0 {
		b.WriteString("\n<b>Restarted:</b> ")
		b.WriteString(strings.Join(restarted, ", "))
		b.WriteString("\n<b>Result:</b> all services recovered")
	}

	if len(suppressed) > 0 {
		b.WriteString("\n<b>Suppressed:</b> ")
		b.WriteString(strings.Join(suppressed, ", "))
	}

	if len(disks) > 0 {
		b.WriteString("\n<b>Disk freed:</b>\n")
		for _, d := range disks {
			b.WriteString("  • ")
			b.WriteString(d)
			b.WriteString("\n")
		}
	}

	return b.String()
}

// mapTriageLevelToAlertLevel converts a triage level string ("WARNING", "CRITICAL", "ERROR")
// to the engine.AlertLevel type used by AutoRemediateDisk.
// The "ERROR" arm is future-proofing: GenerateDiskAlerts currently only emits AlertCritical/AlertWarning,
// but if AlertError disk lines are added the conservative path is to treat them as critical.
func mapTriageLevelToAlertLevel(triageLevel string) engine.AlertLevel {
	switch triageLevel {
	case "WARNING":
		return engine.AlertWarning
	case "WARNING_HIGH":
		return engine.AlertWarningHigh
	case "CRITICAL", "ERROR":
		return engine.AlertCritical
	default:
		return engine.AlertInfo
	}
}

// diskRemediateShouldNotify returns true when the remediation result is worth
// notifying the operator about: either something meaningful was freed (≥ minFreedToNotifyMB)
// or there were errors (operator must see failures).
func diskRemediateShouldNotify(res *engine.DiskRemediateResult) bool {
	if len(res.Errors) > 0 {
		return true
	}
	return sumDiskFreedMB(res) >= minFreedToNotifyMB
}

// sumDiskFreedMB totals freed MB across all cleanup targets in the result.
// Uses CleanupTarget.FreedMB (typed, authoritative) — never parses CleanupTarget.Freed
// display strings, which may contain non-standard formats such as "1.2 GB (4 images)".
func sumDiskFreedMB(res *engine.DiskRemediateResult) float64 {
	var total float64
	for _, tgt := range res.Targets {
		total += tgt.FreedMB
	}
	return total
}

// formatDiskRemediation builds a one-line summary for a disk auto-remediation result.
func formatDiskRemediation(issue engine.TriageIssue, res *engine.DiskRemediateResult) string {
	var parts []string
	for _, tgt := range res.Targets {
		if tgt.Freed != "" && tgt.Freed != "0.0 MB" {
			parts = append(parts, fmt.Sprintf("%s=%s", tgt.Name, tgt.Freed))
		}
	}
	line := issue.Description
	if len(parts) > 0 {
		line += ": " + strings.Join(parts, ", ")
	}
	if res.Docker != "" {
		line += ", docker prune ran"
	}
	return line
}
