package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

const (
	// postRestartVerifyDelay is how long to wait after restart before re-checking service status.
	postRestartVerifyDelay = 10 * time.Second
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

	var restarted []string
	var suppressed []string
	var remediatedDisks []string
	var unhandled []engine.TriageIssue

	for _, issue := range issues {
		level := extractIssueLevel(triageResult, issue.Service)

		// Disk pressure → auto-prune journals/tmp/caches (and docker on CRITICAL).
		if issue.Service == "disk" {
			alertLevel := mapTriageLevelToAlertLevel(level)
			res := eng.AutoRemediateDisk(ctx, alertLevel)
			if res != nil && len(res.Errors) == 0 {
				remediatedDisks = append(remediatedDisks, formatDiskRemediation(issue, res))
			} else {
				if res != nil && len(res.Errors) > 0 {
					slog.Warn("disk auto-remediate partial",
						slog.String("service", issue.Service),
						slog.Any("errors", res.Errors),
						slog.Any("targets", res.Targets),
					)
				}
				unhandled = append(unhandled, issue)
			}
			// Always log the full remediation result at DEBUG so operators can inspect
			// what was actually freed — visible even when there are no errors.
			if res != nil {
				slog.Debug("disk auto-remediate result",
					slog.Any("targets", res.Targets),
					slog.String("docker", res.Docker),
				)
			}
			continue
		}

		// CRITICAL: exited/dead/restarting → restart.
		if level == "CRITICAL" {
			result := eng.RestartService(ctx, issue.Service)
			if result.Success {
				restarted = append(restarted, issue.Service)
			} else {
				unhandled = append(unhandled, issue)
			}
			continue
		}

		// Known benign WARNING/ERROR → suppress.
		if reason, ok := cfg.SuppressWarnings[issue.Service]; ok {
			suppressed = append(suppressed, issue.Service+" ("+reason+")")
			slog.Info("gateway watch: suppressed benign warning",
				slog.String("service", issue.Service),
				slog.String("reason", reason))
			continue
		}

		// Unknown issue → unhandled.
		unhandled = append(unhandled, issue)
	}

	// Post-restart verification.
	verified, failedRestarts := verifyRestarts(ctx, eng, restarted)
	unhandled = append(unhandled, failedRestarts...)

	if len(unhandled) > 0 {
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
		return false
	}

	if len(verified) > 0 || len(remediatedDisks) > 0 {
		// Notify when restarts happened or disk was freed — suppressed-only events are silent.
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
		for _, level := range []string{"CRITICAL", "ERROR", "WARNING"} {
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
	case "CRITICAL", "ERROR":
		return engine.AlertCritical
	default:
		return engine.AlertInfo
	}
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
