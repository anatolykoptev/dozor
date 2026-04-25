package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/dozor/internal/quotas"
)

// runQuotasWatch polls vendor APIs on cfg.Interval and exposes
// vendor_quota_remaining_pct gauges + sends telegram alerts on threshold.
// Mirrors the runGatewayWatch lifecycle pattern: initial tick then ticker loop.
func runQuotasWatch(ctx context.Context, cfg quotas.Config, notify func(string)) {
	if !cfg.Enabled() {
		slog.Info("quotas watch: no vendor keys configured, disabled")
		return
	}
	slog.Info("quotas watch started", slog.String("interval", cfg.Interval.String()))

	runner := quotas.NewRunner(cfg, notify)
	runner.Tick(ctx)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("quotas watch stopped")
			return
		case <-ticker.C:
			runner.Tick(ctx)
		}
	}
}
