package engine

import (
	"testing"
	"time"
)

// TestCalculateHealth_WarningHighIsDegraded verifies that a report containing
// exactly one AlertWarningHigh alert and no other alerts produces
// OverallHealth != "healthy". Prior to the fix, AlertWarningHigh fell through
// to the healthHealthy branch, so Telegram consumers saw healthy for an 88% disk.
func TestCalculateHealth_WarningHighIsDegraded(t *testing.T) {
	t.Parallel()

	r := &DiagnosticReport{
		Timestamp: time.Now(),
		Server:    "test",
		Alerts: []Alert{
			{
				Level:       AlertWarningHigh,
				Service:     "disk",
				Title:       "disk at 88%",
				Description: "disk usage above warning_high threshold",
				Timestamp:   time.Now(),
			},
		},
	}
	// All services running — health should be determined by alerts alone.
	r.Services = []ServiceStatus{
		{Name: "test-svc", State: StateRunning},
	}

	r.CalculateHealth()

	if r.OverallHealth == healthHealthy {
		t.Errorf("CalculateHealth: OverallHealth = %q, want != %q — AlertWarningHigh must not produce healthy", r.OverallHealth, healthHealthy)
	}
}

// TestNeedsAttention_TrueOnWarningHigh verifies that NeedsAttention returns true
// when OverallHealth reflects an AlertWarningHigh condition.
func TestNeedsAttention_TrueOnWarningHigh(t *testing.T) {
	t.Parallel()

	r := &DiagnosticReport{
		Timestamp: time.Now(),
		Server:    "test",
		Alerts: []Alert{
			{
				Level:       AlertWarningHigh,
				Service:     "disk",
				Title:       "disk at 88%",
				Description: "disk usage above warning_high threshold",
				Timestamp:   time.Now(),
			},
		},
		Services: []ServiceStatus{
			{Name: "test-svc", State: StateRunning},
		},
	}

	r.CalculateHealth()

	if !r.NeedsAttention() {
		t.Errorf("NeedsAttention: got false, want true — AlertWarningHigh must cause NeedsAttention=true (OverallHealth=%q)", r.OverallHealth)
	}
}
