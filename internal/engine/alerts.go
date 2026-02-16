package engine

import (
	"fmt"
	"time"
)

// AlertGenerator creates alerts from service statuses.
type AlertGenerator struct {
	cfg Config
}

// GenerateAlerts checks service statuses and returns relevant alerts.
func (g *AlertGenerator) GenerateAlerts(statuses []ServiceStatus) []Alert {
	var alerts []Alert
	now := time.Now()

	for _, s := range statuses {
		// Service not running
		if s.State != StateRunning {
			alerts = append(alerts, Alert{
				Level:           AlertCritical,
				Service:         s.Name,
				Title:           fmt.Sprintf("%s is %s", s.Name, s.State),
				Description:     fmt.Sprintf("Container %s is in %s state", s.Name, s.State),
				SuggestedAction: fmt.Sprintf("Check logs: docker compose logs --tail 50 %s", s.Name),
				Timestamp:       now,
			})
		}

		// High restart count
		if s.RestartCount >= g.cfg.RestartThreshold {
			alerts = append(alerts, Alert{
				Level:           AlertError,
				Service:         s.Name,
				Title:           fmt.Sprintf("%s has restarted %d times", s.Name, s.RestartCount),
				Description:     fmt.Sprintf("Container %s has restarted %d times (threshold: %d)", s.Name, s.RestartCount, g.cfg.RestartThreshold),
				SuggestedAction: "Check logs for crash reasons. Consider increasing memory/CPU limits.",
				Timestamp:       now,
			})
		}

		// High CPU
		if s.CPUPercent != nil && *s.CPUPercent >= g.cfg.CPUThreshold {
			alerts = append(alerts, Alert{
				Level:           AlertWarning,
				Service:         s.Name,
				Title:           fmt.Sprintf("%s CPU at %.1f%%", s.Name, *s.CPUPercent),
				Description:     fmt.Sprintf("Container CPU usage %.1f%% exceeds threshold %.1f%%", *s.CPUPercent, g.cfg.CPUThreshold),
				SuggestedAction: "Investigate high CPU usage. Consider scaling or optimizing.",
				Timestamp:       now,
			})
		}

		// High memory
		if s.MemoryMB != nil && s.MemoryLimitMB != nil && *s.MemoryLimitMB > 0 {
			pct := (*s.MemoryMB / *s.MemoryLimitMB) * 100
			if pct >= g.cfg.MemoryThreshold {
				alerts = append(alerts, Alert{
					Level:           AlertWarning,
					Service:         s.Name,
					Title:           fmt.Sprintf("%s memory at %.1f%%", s.Name, pct),
					Description:     fmt.Sprintf("Container memory usage %.0fMB/%.0fMB (%.1f%%)", *s.MemoryMB, *s.MemoryLimitMB, pct),
					SuggestedAction: "Check for memory leaks. Consider increasing memory limit.",
					Timestamp:       now,
				})
			}
		}

		// Error count
		if s.ErrorCount >= g.cfg.ErrorThreshold {
			alerts = append(alerts, Alert{
				Level:           AlertError,
				Service:         s.Name,
				Title:           fmt.Sprintf("%s has %d errors", s.Name, s.ErrorCount),
				Description:     fmt.Sprintf("Container has %d errors in recent logs (threshold: %d)", s.ErrorCount, g.cfg.ErrorThreshold),
				SuggestedAction: "Analyze logs: server_inspect({mode: 'analyze', service: '" + s.Name + "'})",
				Timestamp:       now,
			})
		}
	}

	return alerts
}

// GenerateDiskAlerts creates alerts for disk pressure conditions.
func GenerateDiskAlerts(pressures []DiskPressure, cfg Config) []Alert {
	var alerts []Alert
	now := time.Now()
	for _, p := range pressures {
		if p.UsedPct >= cfg.DiskCritical {
			alerts = append(alerts, Alert{
				Level:           AlertCritical,
				Service:         "disk",
				Title:           fmt.Sprintf("Disk %s at %.0f%%", p.MountPoint, p.UsedPct),
				Description:     fmt.Sprintf("%s: %.0f%% used, %.1fGB free", p.Filesystem, p.UsedPct, p.AvailGB),
				SuggestedAction: "Run server_cleanup({report: true}) to scan reclaimable space.",
				Timestamp:       now,
			})
		} else if p.UsedPct >= cfg.DiskThreshold {
			alerts = append(alerts, Alert{
				Level:           AlertWarning,
				Service:         "disk",
				Title:           fmt.Sprintf("Disk %s at %.0f%%", p.MountPoint, p.UsedPct),
				Description:     fmt.Sprintf("%s: %.0f%% used, %.1fGB free", p.Filesystem, p.UsedPct, p.AvailGB),
				SuggestedAction: "Monitor disk usage. Run server_cleanup({report: true}) to check reclaimable space.",
				Timestamp:       now,
			})
		}
	}
	return alerts
}
