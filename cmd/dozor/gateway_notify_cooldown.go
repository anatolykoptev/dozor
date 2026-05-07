package main

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// notifyCooldownDuration is the default per-hash notify cooldown.
// Overridable via DOZOR_NOTIFY_COOLDOWN env var.
const notifyCooldownDuration = 1 * time.Hour

// notifyCooldown suppresses repeated Telegram notifications for the same set of
// issues (identified by hash) within the cooldown window.
// This prevents spam when a persistent issue (e.g. disk at 82%, can't drop below
// 80% threshold) re-triggers the watch loop after the remediation cooldown expires.
type notifyCooldown struct {
	mu       sync.Mutex
	lastSent map[string]time.Time // key: hash
	duration time.Duration        // 0 = disabled
}

// newNotifyCooldown creates a cooldown tracker with the given window duration.
func newNotifyCooldown(d time.Duration) *notifyCooldown {
	return &notifyCooldown{
		lastSent: make(map[string]time.Time),
		duration: d,
	}
}

// newNotifyCooldownFromEnv creates a cooldown tracker, reading the duration from
// the DOZOR_NOTIFY_COOLDOWN env var (Go duration string, e.g. "1h" or "30m").
// "0" or "0s" disables the cooldown entirely.
// Falls back to notifyCooldownDuration if the env var is absent or unparseable;
// logs a WARN on parse failure.
func newNotifyCooldownFromEnv() *notifyCooldown {
	d := notifyCooldownDuration
	if raw := os.Getenv("DOZOR_NOTIFY_COOLDOWN"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= 0 {
			d = parsed
		} else if err != nil {
			slog.Warn("invalid DOZOR_NOTIFY_COOLDOWN, falling back to default", //nolint:gosec // raw is an operator-supplied env var, not user input
				"value", raw, "default", d, "error", err)
		}
	}
	slog.Info("notify cooldown configured", "duration", d) //nolint:gosec // duration is time.Duration, not user input
	return newNotifyCooldown(d)
}

// shouldSuppress returns true when this hash was already sent within the cooldown
// window relative to now.
// When duration is 0, the cooldown is disabled and this always returns false.
func (n *notifyCooldown) shouldSuppress(hash string, now time.Time) bool {
	if n.duration == 0 {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	last, ok := n.lastSent[hash]
	if !ok {
		return false
	}
	return now.Sub(last) < n.duration
}

// markSent records that a notification for this hash was sent at now.
func (n *notifyCooldown) markSent(hash string, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastSent[hash] = now
}
