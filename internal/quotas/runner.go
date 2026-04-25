package quotas

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anatolykoptev/dozor/internal/quotas/probe"
)

const (
	// thresholdWarn is the remaining percentage at which a warning alert fires.
	thresholdWarn = 20.0
	// thresholdPage is the remaining percentage at which a critical page fires.
	thresholdPage = 5.0
	// failureAlertWindow is how long a probe must fail continuously before alerting.
	failureAlertWindow = 15 * time.Minute
)

// vendorState tracks per-vendor alert dedup state.
type vendorState struct {
	lastAlertLevel int       // 0=none, 1=warn, 2=page
	failingSince   time.Time // zero if not failing
	failAlerted    bool      // true once the 15min failure alert fired
}

// Runner orchestrates all configured vendor probers, updates Prometheus metrics,
// and fires threshold-based telegram alerts via notify.
type Runner struct {
	probers []probe.Prober
	notify  func(string)
	states  map[string]*vendorState
}

// NewRunner builds a Runner from cfg and wires notify for alert delivery.
func NewRunner(cfg Config, notify func(string)) *Runner {
	var probers []probe.Prober

	if p := probe.NewWebshare(cfg.WebshareAPIKey); p != nil {
		probers = append(probers, p)
		slog.Info("quotas: webshare probe enabled")
	}
	if p := probe.NewGitHub(cfg.GitHubPAT); p != nil {
		probers = append(probers, p)
		slog.Info("quotas: github probe enabled")
	}
	if p := probe.NewAnthropic(cfg.AnthropicAdminKey, cfg.AnthropicOrgID); p != nil {
		probers = append(probers, p)
		slog.Info("quotas: anthropic probe enabled")
	}
	if p := probe.NewGemini(cfg.GeminiAPIKey); p != nil {
		probers = append(probers, p)
		slog.Info("quotas: gemini probe enabled")
	}

	if len(probers) == 0 {
		slog.Warn("quotas: no vendors configured")
	}

	states := make(map[string]*vendorState, len(probers))
	for _, p := range probers {
		states[p.Vendor()] = &vendorState{}
	}

	return &Runner{
		probers: probers,
		notify:  notify,
		states:  states,
	}
}

// Tick runs all probers once, updates metrics, and fires alerts as needed.
func (r *Runner) Tick(ctx context.Context) {
	for _, p := range r.probers {
		r.runOne(ctx, p)
	}
}

func (r *Runner) runOne(ctx context.Context, p probe.Prober) {
	vendor := p.Vendor()
	state := r.states[vendor]

	probeCtx, cancel := context.WithTimeout(ctx, probe.ProbeTimeout)
	defer cancel()

	start := time.Now()
	readings, err := p.Probe(probeCtx)
	elapsed := time.Since(start).Seconds()

	CheckDuration.WithLabelValues(vendor).Observe(elapsed)

	if err != nil {
		reason := probe.FailureReason(err)
		CheckFailures.WithLabelValues(vendor, reason).Inc()
		slog.Warn("quotas: probe failed",
			slog.String("vendor", vendor),
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		r.handleFailure(vendor, state)
		return
	}

	// Probe succeeded — reset failure window.
	state.failingSince = time.Time{}
	state.failAlerted = false

	for _, reading := range readings {
		QuotaRemaining.WithLabelValues(vendor, reading.Product).Set(reading.Remaining)
		slog.Info("quotas: probe ok",
			slog.String("vendor", vendor),
			slog.String("product", reading.Product),
			slog.Float64("remaining_pct", reading.Remaining),
		)
		r.checkThreshold(vendor, reading, state)
	}
}

// handleFailure tracks sustained probe failures and alerts after failureAlertWindow.
func (r *Runner) handleFailure(vendor string, state *vendorState) {
	if state.failingSince.IsZero() {
		state.failingSince = time.Now()
		return
	}
	if !state.failAlerted && time.Since(state.failingSince) >= failureAlertWindow {
		state.failAlerted = true
		msg := fmt.Sprintf("⚠️ Quota probe failure: %s has been failing for >15 min. "+
			"Check API key and vendor status.", vendor)
		slog.Warn("quotas: sustained probe failure alert", slog.String("vendor", vendor))
		r.notify(msg)
	}
}

// checkThreshold fires telegram alerts when quota crosses 20% (warn) or 5% (page).
// Dedup: only fires once per level, resets if quota goes back above the threshold.
func (r *Runner) checkThreshold(vendor string, reading probe.Reading, state *vendorState) {
	pct := reading.Remaining
	product := reading.Product

	level := 0
	if pct <= thresholdPage {
		level = 2
	} else if pct <= thresholdWarn {
		level = 1
	}

	if level == 0 {
		// Quota healthy — reset alert state.
		state.lastAlertLevel = 0
		return
	}

	if level <= state.lastAlertLevel {
		// Already alerted at this level or higher; don't re-fire.
		return
	}

	state.lastAlertLevel = level

	var msg string
	switch level {
	case 1:
		msg = fmt.Sprintf("⚠️ %s %s quota: %.0f%% remaining. "+
			"Consider topping up to prevent disruption.",
			vendorDisplayName(vendor), product, pct)
	case 2:
		msg = fmt.Sprintf("🚨 CRITICAL: %s %s quota: %.0f%% remaining. "+
			"Immediate top-up required — service disruption imminent.",
			vendorDisplayName(vendor), product, pct)
	}

	slog.Warn("quotas: threshold alert",
		slog.String("vendor", vendor),
		slog.String("product", product),
		slog.Float64("remaining_pct", pct),
		slog.Int("level", level),
	)
	r.notify(msg)
}

// vendorDisplayName maps vendor identifiers to human-readable names.
func vendorDisplayName(vendor string) string {
	switch vendor {
	case "webshare":
		return "Webshare bandwidth"
	case "github":
		return "GitHub Actions"
	case "anthropic":
		return "Anthropic spend"
	case "gemini":
		return "Gemini API"
	default:
		return vendor
	}
}
