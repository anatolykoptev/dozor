// Package probe defines the Prober interface and shared helper types.
package probe

import (
	"context"
	"time"
)

// Result holds one or more quota readings from a single vendor probe.
type Result struct {
	Vendor   string
	Readings []Reading
}

// Reading is a single quota data point.
type Reading struct {
	Product   string  // e.g. "bandwidth", "requests", "actions_minutes"
	Remaining float64 // 0-100 percent remaining
}

// Prober is the interface each vendor implements.
type Prober interface {
	// Vendor returns a short identifier used as the "vendor" label.
	Vendor() string
	// Probe performs the API call and returns quota readings.
	// It must respect context cancellation for timeout handling.
	Probe(ctx context.Context) ([]Reading, error)
}

const (
	// ProbeTimeout is the per-probe HTTP timeout.
	ProbeTimeout = 15 * time.Second
	// reasonHTTPErr is the Prometheus label value for HTTP errors.
	reasonHTTPErr = "http_err"
)
