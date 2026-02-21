package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Watch runs periodic health checks and sends webhook alerts on changes.
func Watch(ctx context.Context, cfg Config) error {
	agent := NewAgent(cfg)
	var prevHash string

	ft := NewFailureTracker(cfg.AlertConfirmCount)
	fd := NewFlapDetector(cfg.FlapWindow, cfg.FlapHigh, cfg.FlapLow)

	slog.Info("watch mode started",
		slog.String("interval", cfg.WatchInterval.String()),
		slog.String("webhook", cfg.WebhookURL))

	// Run check immediately
	prevHash = runCheck(ctx, agent, cfg, prevHash, ft, fd)

	ticker := time.NewTicker(cfg.WatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("watch mode stopped")
			return nil
		case <-ticker.C:
			prevHash = runCheck(ctx, agent, cfg, prevHash, ft, fd)
		}
	}
}

func runCheck(ctx context.Context, agent *ServerAgent, cfg Config, prevHash string, ft *FailureTracker, fd *FlapDetector) string {
	slog.Info("running health check")

	report := agent.Diagnose(ctx, nil)

	// Disk pressure monitoring
	pressures := agent.GetDiskPressure(ctx)
	diskAlerts := GenerateDiskAlerts(pressures, cfg)
	report.Alerts = append(report.Alerts, diskAlerts...)

	// Remote server monitoring
	if cfg.HasRemote() {
		remote := CheckRemoteServer(ctx, cfg)
		if remote != nil {
			report.Alerts = append(report.Alerts, remote.Alerts...)
		}
	}

	// Filter alerts through confirmation + flap detection.
	report.Alerts = filterAlerts(report.Alerts, ft, fd)

	report.CalculateHealth()

	// Hash the alerts to detect changes
	alertHash := hashAlerts(report.Alerts)

	if alertHash == prevHash {
		slog.Info("no changes in alerts", slog.String("health", report.OverallHealth))
		return alertHash
	}

	slog.Info("alert state changed",
		slog.String("health", report.OverallHealth),
		slog.Int("alerts", len(report.Alerts)))

	// Send webhook if configured
	if cfg.WebhookURL != "" {
		text := FormatReport(report)
		if err := sendWebhook(ctx, cfg.WebhookURL, text); err != nil {
			slog.Error("webhook failed", slog.Any("error", err))
		} else {
			slog.Info("webhook sent")
		}
	}

	return alertHash
}

// filterAlerts runs alerts through FailureTracker and FlapDetector.
// Alerts that are not yet confirmed or from flapping services are suppressed.
// Services with no alerts get their success recorded to reset counters.
func filterAlerts(alerts []Alert, ft *FailureTracker, fd *FlapDetector) []Alert {
	// Track which services have alerts this cycle.
	alertedKeys := make(map[string]bool)
	var confirmed []Alert

	for _, a := range alerts {
		key := AlertKey(a)
		alertedKeys[key] = true

		// Record failure in flap detector.
		flapStatus := fd.Record(key, false)
		if flapStatus.Flapping {
			slog.Info("alert suppressed: flapping detected",
				slog.String("service", a.Service),
				slog.Float64("change_rate", flapStatus.ChangeRate))
			continue
		}

		// Check consecutive failure confirmation.
		ok, count := ft.RecordFailure(key)
		if !ok {
			slog.Info("alert suppressed: awaiting confirmation",
				slog.String("service", a.Service),
				slog.String("alert", a.Title),
				slog.Int("failures", count),
				slog.Int("threshold", ft.threshold))
			continue
		}

		confirmed = append(confirmed, a)
	}

	return confirmed
}


func hashAlerts(alerts []Alert) string {
	if len(alerts) == 0 {
		return "none"
	}
	h := sha256.New()
	for _, a := range alerts {
		fmt.Fprintf(h, "%s:%s:%s\n", a.Level, a.Service, a.Title)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func sendWebhook(ctx context.Context, url, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
