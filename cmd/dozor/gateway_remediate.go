package main

import (
	"context"
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
	var unhandled []engine.TriageIssue

	for _, issue := range issues {
		level := extractIssueLevel(triageResult, issue.Service)

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
		slog.Info("gateway watch: auto-remediation partial, routing remaining to agent",
			slog.Int("restarted", len(verified)),
			slog.Int("suppressed", len(suppressed)),
			slog.Int("unhandled", len(unhandled)))
		return false
	}

	if len(verified) > 0 {
		// Only notify when actual restarts happened — suppressed-only events are silent.
		msg := buildAutoRemediateMessage(verified, suppressed)
		if notify != nil {
			notify(msg)
		}
	}
	if len(verified)+len(suppressed) > 0 {
		slog.Info("gateway watch: auto-remediated all issues",
			slog.Int("restarted", len(verified)),
			slog.Int("suppressed", len(suppressed)))
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
func buildAutoRemediateMessage(restarted, suppressed []string) string {
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

	return b.String()
}
