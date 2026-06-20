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

	// DebouncePersistTotal makes the durable-debounce lifecycle observable so a
	// future regression of the VOLATILE-PENDING-STATE class (queued build lost
	// on dozor restart) surfaces as telemetry, not silence.
	//
	// op label values:
	//   "persist"        — one atomic write of the pending set succeeded (per WRITE, not per entry)
	//   "persist_error"  — an atomic write failed (state file may be stale; build still queued in-memory)
	//   "reload_error"   — boot Reload could not read or parse the state file (per RELOAD, not per entry);
	//                      EVERY queued build it held is lost — this is the silent-failure hole on the
	//                      recovery path itself, so a non-zero value must alert
	//   "rearm"          — a recovered entry was re-armed for its remaining window on boot
	//   "fire_on_boot"   — a recovered entry whose deadline elapsed during downtime fired on boot
	//   "stale_skip"     — a recovered entry's commit was already the deployed HEAD; no rebuild
	//
	// Label semantics: "persist", "persist_error" and "reload_error" are
	// per-WHOLE-FILE events with empty repo/service (a single write/read covers
	// the whole pending set, so a per-repo split would double-count unrelated
	// repos). "rearm", "fire_on_boot" and "stale_skip" are per-ENTRY recovery
	// events and carry the real repo/service labels.
	DebouncePersistTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_debounce_persist_total",
		Help: "Durable-debounce lifecycle events (persist/reload/rearm/fire_on_boot/stale_skip) for restart-survival of queued builds.",
	}, []string{"repo", "service", "op"})

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

	// BuildInflight tracks the number of builds currently executing, by class.
	// class label: "heavy" (acquires heavySem) or "light". Useful for alerting
	// when concurrent heavy builds approach the ARM host OOM threshold.
	BuildInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dozor_build_inflight",
		Help: "Builds currently executing, by class (heavy|light).",
	}, []string{"class"})

	// DeployClonePullTotal counts auto-pull attempts on deploy clones before
	// each compose build. outcome label values:
	//   "up_to_date"      — remote had no new commits, nothing to do
	//   "fast_forward"    — clone was successfully fast-forwarded to origin/<branch>
	//   "dirty_skipped"   — clone had local edits; pull skipped, build uses stale compose
	//   "diverged_skipped"— ff-only pull failed (diverged history); build uses current state
	//   "error"           — git command failed unexpectedly; build uses current state
	//
	// If "dirty_skipped" ticks, reconcile the deploy clone manually:
	//   git -C <deploy_clone_path> status && git stash && git pull
	DeployClonePullTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_clone_pull_total",
		Help: "Auto-pull attempts on deploy clones before compose builds, by outcome.",
	}, []string{"repo", "outcome"})

	// ManualDeployTotal counts server_deploy MCP tool invocations (not webhook-driven).
	// Labels:
	//   repo    — full GitHub repo name (owner/name) or "unconfigured" for ad-hoc paths
	//   trigger — "sha_pinned" (normal, origin/<branch> worktree) or "from_disk" (debug opt-out)
	//   result  — "started", "success", "failure"
	//
	// A "started" + "success" pair means the deploy completed in the background.
	// A counter stuck on "started" without "success"/"failure" means the background
	// goroutine is still running (or was killed before it could record the outcome).
	ManualDeployTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_manual_deploy_total",
		Help: "Manual deploys triggered via server_deploy MCP tool, by repo, trigger mode, and result.",
	}, []string{"repo", "trigger", "result"})

	// ManualDeployBranchMismatchTotal counts cases where the source clone's
	// checked-out branch differs from the configured deploy branch.
	// Fires as a WARN signal — the build is still correct (origin/<configured>
	// is always used), but the drift is worth alerting on so operators can
	// reconcile or investigate.
	//
	// Labels:
	//   repo       — full GitHub repo name
	//   configured — the branch from deploy-repos.yaml (e.g. "main")
	//   actual     — the branch the source clone has checked out (e.g. "dev")
	ManualDeployBranchMismatchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_manual_deploy_branch_mismatch_total",
		Help: "Manual deploy: source clone branch ≠ configured deploy branch (build still uses origin/<configured>).",
	}, []string{"repo", "configured", "actual"})

	// DeploySourceSyncTotal counts best-effort source-checkout sync attempts run
	// off the deploy hot path after each build (success or failure). It advances
	// each repo's ~/src/X default-branch ref to origin so go-code indexes fresh
	// and the dev checkout stays current, instead of waiting for the hourly
	// git-sync timer. Default OFF behind DOZOR_DEPLOY_SOURCE_SYNC.
	//
	// result label values:
	//   "up_to_date"           — already at origin, or SourcePath==DeployClonePath guard (no double-pull)
	//   "ff_updated"           — local default-branch ref was fast-forwarded to origin
	//   "skipped_dirty"        — tracked working-tree edits present; left untouched (untracked scratch does NOT block)
	//   "skipped_locked"       — .git/index.lock present (a concurrent build/agent/timer holds the index)
	//   "skipped_disabled"     — DOZOR_DEPLOY_SOURCE_SYNC not set truthy (the default)
	//   "checked_out_elsewhere"— target branch checked out in another worktree; ref left as-is (benign)
	//   "skipped_diverged"     — local default branch diverged / has commits ahead; ff refused (benign)
	//   "error"                — a git command failed unexpectedly; checkout left as-is
	//   "panic"                — the sync goroutine panicked and was recovered (must never happen; alert if seen)
	//
	// The sync is best-effort and NEVER touches dozor_build_result_total — that
	// counter's cadence is the control proving the sync is off the critical path.
	DeploySourceSyncTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_source_sync_total",
		Help: "Best-effort source-checkout (~/src/X) ff-sync attempts after each deploy, by outcome.",
	}, []string{"repo", "result"})
)
