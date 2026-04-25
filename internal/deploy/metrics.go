package deploy

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus counters for webhook-driven deploys.
//
// Naming follows the dozor convention: <subsystem>_<event>_total. Labels are
// kept low-cardinality (repo + service, plus a reason for skips).
var (
	// DebouncedTotal counts webhook events that were absorbed by the debounce
	// window — i.e. arrived while a build for the same key was already pending.
	DebouncedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_debounced_total",
		Help: "Webhook events deferred or coalesced by the per-service debounce window.",
	}, []string{"repo", "service"})

	// SkippedTotal counts deploys that were skipped before queueing.
	// `reason` is one of: "no_relevant_paths", "explicit_skip".
	SkippedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_skipped_total",
		Help: "Deploys skipped before reaching the build queue.",
	}, []string{"repo", "service", "reason"})

	// FiredTotal counts deploys actually dispatched after debounce / filtering.
	FiredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_fired_total",
		Help: "Deploys dispatched to the build queue after path filtering and debounce.",
	}, []string{"repo", "service"})
)
