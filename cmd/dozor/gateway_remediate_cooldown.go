package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// remediateCooldownDuration is the default per-(service,level) cooldown.
// Overridable via DOZOR_REMEDIATE_COOLDOWN env var.
const remediateCooldownDuration = 30 * time.Minute

// bytesPerMB is 1024*1024, used for converting MB to bytes in Prometheus counters.
const bytesPerMB = 1024 * 1024

// topDirsN is the number of top directories to log when cleanup freed nothing.
const topDirsN = 5

// remediateMetrics holds Prometheus counters for disk auto-remediation.
// Registered once at package init via promauto.
var remediateMetrics = struct {
	attemptsTotal  *prometheus.CounterVec
	freedBytesTotal *prometheus.CounterVec
	cooldownSkipped *prometheus.CounterVec
}{
	attemptsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_remediate_attempts_total",
		Help: "Total auto-remediation attempts by action and result.",
	}, []string{"action", "result"}),
	freedBytesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_remediate_freed_bytes_total",
		Help: "Total bytes freed by auto-remediation, by action and result.",
	}, []string{"action", "result"}),
	cooldownSkipped: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_remediate_cooldown_skipped_total",
		Help: "Total auto-remediation runs skipped due to active cooldown, by service and level.",
	}, []string{"service", "level"}),
}

// remediateCooldown tracks the last auto-remediate run time per (service, level) pair.
type remediateCooldown struct {
	mu       sync.Mutex
	lastRun  map[string]time.Time
	duration time.Duration
}

// newRemediateCooldown creates a cooldown tracker with the given window duration.
func newRemediateCooldown(d time.Duration) *remediateCooldown {
	return &remediateCooldown{
		lastRun:  make(map[string]time.Time),
		duration: d,
	}
}

// newRemediateCooldownFromEnv creates a cooldown tracker, reading the duration
// from the DOZOR_REMEDIATE_COOLDOWN env var (Go duration string, e.g. "30m").
// "0" or "0s" disables the cooldown entirely.
// Falls back to remediateCooldownDuration if the env var is absent or unparseable;
// logs a WARN on parse failure.
func newRemediateCooldownFromEnv() *remediateCooldown {
	d := remediateCooldownDuration
	if raw := os.Getenv("DOZOR_REMEDIATE_COOLDOWN"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= 0 {
			d = parsed
		} else if err != nil {
			slog.Warn("invalid DOZOR_REMEDIATE_COOLDOWN, falling back to default",
				"value", raw, "default", d, "error", err)
		}
	}
	slog.Info("auto-remediate cooldown configured", "duration", d)
	return newRemediateCooldown(d)
}

// shouldSkip returns true when a run for this (service, level) pair happened
// within the cooldown window relative to now.
// When duration is 0, the cooldown is disabled and this always returns false.
func (c *remediateCooldown) shouldSkip(service, level string, now time.Time) bool {
	if c.duration == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.lastRun[service+":"+level]
	if !ok {
		return false
	}
	return now.Sub(last) < c.duration
}

// markRun records that a run for (service, level) just happened at now.
func (c *remediateCooldown) markRun(service, level string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastRun[service+":"+level] = now
}

// handleDiskIssueWithCooldown is the clock-injectable version of handleDiskIssue that
// applies cooldown before calling AutoRemediateDisk, and emits structured log events
// and Prometheus counters for observability.
//
// When notify is suppressed (freed < threshold, no errors) it logs a "top 5 dirs on /"
// snapshot at WARN so the operator can grep when needed.
func handleDiskIssueWithCooldown(
	ctx context.Context,
	rem diskRemediator,
	issue engine.TriageIssue,
	level string,
	cd *remediateCooldown,
	now time.Time,
) (notifyMsg string, handled bool) {
	if cd.shouldSkip(issue.Service, level, now) {
		slog.InfoContext(ctx, "auto-remediate cooldown active, skipping",
			slog.String("service", issue.Service),
			slog.String("level", level),
		)
		remediateMetrics.cooldownSkipped.WithLabelValues(issue.Service, level).Inc()
		return "", true // counted as handled — no need to escalate to LLM
	}

	alertLevel := mapTriageLevelToAlertLevel(level)
	res := rem.AutoRemediateDisk(ctx, alertLevel)

	// Mark cooldown regardless of how much was freed — the point is to stop
	// re-running cleanup on the same empty caches every 5-min tick.
	cd.markRun(issue.Service, level, now)

	if res == nil {
		remediateMetrics.attemptsTotal.WithLabelValues("disk", "nil").Inc()
		return "", false
	}

	freedMB := sumDiskFreedMB(res)
	result := "ok"
	if len(res.Errors) > 0 {
		result = "partial"
	}

	remediateMetrics.attemptsTotal.WithLabelValues("disk", result).Inc()
	remediateMetrics.freedBytesTotal.WithLabelValues("disk", result).Add(freedMB * bytesPerMB)

	slog.InfoContext(ctx, "dozor.remediate.freed",
		slog.String("action", "disk"),
		slog.String("result", result),
		slog.Float64("freed_mb", freedMB),
	)

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
		msg := formatDiskRemediation(issue, res) + "\n[ERRORS] " + strings.Join(res.Errors, "; ")
		return msg, true
	}

	if !diskRemediateShouldNotify(res) {
		slog.InfoContext(ctx, "auto-remediate ran but freed nothing meaningful, suppressing notify",
			slog.Float64("freed_mb", freedMB),
			slog.Int("targets_attempted", len(res.Targets)),
		)
		// Log top dirs so operator can grep when manual investigation is needed.
		// We call the engine directly via transport — requires *engine.ServerAgent.
		// For the interface-typed rem we skip top-dirs (test stubs don't have transport).
		if sa, ok := rem.(topDirsReporter); ok {
			if dirs, err := sa.TopDirsRoot(ctx, topDirsN); err == nil && len(dirs) > 0 {
				slog.WarnContext(ctx, "auto-remediate freed nothing meaningful — manual investigation likely needed",
					slog.Float64("freed_mb", freedMB),
					slog.Any("top_dirs", dirs),
				)
			}
		}
		return "", true
	}

	return formatDiskRemediation(issue, res), true
}

// topDirsReporter is satisfied by *engine.ServerAgent when TopDirsRoot is wired.
type topDirsReporter interface {
	TopDirsRoot(ctx context.Context, n int) ([]engine.DirSize, error)
}
