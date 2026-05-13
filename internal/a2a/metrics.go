// Metrics for the A2A executor. Concurrency cap rejections and saturation
// gauge — added 2026-05-12 after the 6.3 GB RSS incident.
package a2a

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ExecutorRejected counts A2A requests rejected by the concurrency cap.
	ExecutorRejected = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dozor_a2a_executor_rejected_total",
		Help: "Total A2A requests rejected because the concurrency semaphore was at capacity.",
	})

	// ExecutorInflight tracks goroutines currently running e.proc.Process.
	ExecutorInflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "dozor_a2a_executor_inflight",
		Help: "Number of A2A agent runs currently in-flight (semaphore slots held).",
	})

	// ExecutorCap exposes the configured semaphore capacity for dashboards.
	ExecutorCap = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "dozor_a2a_executor_cap",
		Help: "Configured A2A executor concurrency cap (DOZOR_A2A_MAX_CONCURRENT).",
	})
)
