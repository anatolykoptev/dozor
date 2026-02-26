package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// runRemoteWatch runs fast, independent remote server checks on a short interval.
// Sends CRITICAL/ERROR alerts directly to Telegram, bypassing the LLM pipeline.
func runRemoteWatch(ctx context.Context, cfg engine.Config, notify func(string)) {
	interval := cfg.RemoteCheckInterval
	slog.Info("remote watch started",
		slog.String("interval", interval.String()),
		slog.String("url", cfg.RemoteURL),
		slog.String("host", cfg.RemoteHost))

	const repeatInterval = 30 * time.Minute

	// Track last alert hash and when it was last sent.
	var lastAlertHash string
	var lastAlertTime time.Time

	// Consecutive failure confirmation + flap detection.
	ft := engine.NewFailureTracker(cfg.AlertConfirmCount)
	fd := engine.NewFlapDetector(cfg.FlapWindow, cfg.FlapHigh, cfg.FlapLow)

	time.Sleep(15 * time.Second) // let services boot

	remoteCheckTick(ctx, cfg, notify, &lastAlertHash, &lastAlertTime, repeatInterval, ft, fd)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("remote watch stopped")
			return
		case <-ticker.C:
			remoteCheckTick(ctx, cfg, notify, &lastAlertHash, &lastAlertTime, repeatInterval, ft, fd)
		}
	}
}

func remoteCheckTick(ctx context.Context, cfg engine.Config, notify func(string), lastHash *string, lastTime *time.Time, repeatAfter time.Duration, ft *engine.FailureTracker, fd *engine.FlapDetector) {
	checkCtx, cancel := context.WithTimeout(ctx, remoteCheckTimeoutSec*time.Second)
	defer cancel()

	status := engine.CheckRemoteServer(checkCtx, cfg)
	if status == nil {
		return
	}

	if len(status.Alerts) == 0 {
		// Healthy — reset confirmation and flap state.
		if *lastHash != "" {
			slog.Info("remote watch: recovered, clearing alert state")
			*lastHash = ""
		}
		// Record success for all previously tracked keys.
		ft.RecordSuccess("remote")
		fd.Record("remote", true)
		slog.Info("remote watch: healthy")
		return
	}

	// Run alerts through confirmation + flap detection.
	flapStatus := fd.Record("remote", false)
	if flapStatus.Flapping {
		slog.Info("remote watch: alert suppressed (flapping)",
			slog.Float64("change_rate", flapStatus.ChangeRate))
		return
	}

	confirmed, count := ft.RecordFailure("remote")
	if !confirmed {
		slog.Info("remote watch: alert suppressed (awaiting confirmation)",
			slog.Int("failures", count),
			slog.Int("threshold", cfg.AlertConfirmCount))
		return
	}

	msg := engine.FormatRemoteAlerts(status)
	if msg == "" {
		return
	}

	h := sha256.Sum256([]byte(msg))
	hash := hex.EncodeToString(h[:8])

	now := time.Now()
	if hash == *lastHash && now.Sub(*lastTime) < repeatAfter {
		slog.Info("remote watch: same alert, suppressed (dedup)")
		return
	}

	// New alert or repeat interval elapsed.
	*lastHash = hash
	*lastTime = now

	slog.Warn("remote watch: alerting", slog.String("hash", hash))
	notify(msg)
}
