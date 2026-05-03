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

	// DeduplicatedTotal counts deploys that fired correctly (passed debounce +
	// path filtering) but were dropped at queue admission because a build for
	// the same service set was already queued or in-flight. The newer commit
	// is silently absorbed — by design, to keep CPU off the build host when
	// bursts of webhooks arrive during an existing build. This counter makes
	// the silent path observable so a dashboard or alert can flag when a fix
	// commit was dedup'd against an earlier build of the same service (the
	// operator has to manually retrigger in that case).
	DeduplicatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_deduplicated_total",
		Help: "Fired deploys dropped at queue admission because an exact-SHA duplicate was already queued or in-flight (e.g. webhook retry).",
	}, []string{"repo", "service"})

	// SupersededTotal counts pending builds that were replaced by a newer commit
	// before they ran. Newest-wins coalescing: when a webhook arrives for a service
	// that already has a different SHA pending, the older one is dropped. This is
	// expected behaviour for cascading merges; high rate suggests a debounce
	// window that's too short for the merge pace.
	SupersededTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_superseded_total",
		Help: "Pending builds replaced by a newer commit before they ran (newest-wins coalescing).",
	}, []string{"repo", "service"})

	// BuildResultTotal counts completed builds by status (success, failure, timeout).
	// Labels: repo (anatolykoptev/repo-name), service (service name), status (success|failure|timeout).
	BuildResultTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_build_result_total",
		Help: "Build results by repository, service, and outcome status.",
	}, []string{"repo", "service", "status"})
)
