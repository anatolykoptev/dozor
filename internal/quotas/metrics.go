// Package quotas registers Prometheus gauges, histograms, and counters
// for vendor quota probes.
package quotas

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// QuotaRemaining is a gauge for remaining quota as a percentage (0-100).
	QuotaRemaining = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vendor_quota_remaining_pct",
		Help: "Remaining vendor quota as a percentage (0-100). Updated every probe interval.",
	}, []string{"vendor", "product"})

	// CheckDuration is a histogram for probe round-trip time.
	CheckDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vendor_quota_check_duration_seconds",
		Help:    "Time taken to complete a single vendor quota probe.",
		Buckets: prometheus.DefBuckets,
	}, []string{"vendor"})

	// CheckFailures counts probe failures by reason: auth_fail, http_err, parse_err, timeout.
	CheckFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vendor_quota_check_failures_total",
		Help: "Total vendor quota probe failures, partitioned by vendor and failure reason.",
	}, []string{"vendor", "reason"})
)
