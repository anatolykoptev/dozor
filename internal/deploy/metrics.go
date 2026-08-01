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

	// QueuePersistTotal makes the durable-BUILD-QUEUE lifecycle observable so a
	// future regression of the VOLATILE-PENDING-STATE class AT THE QUEUE LAYER
	// (a queued-or-in-flight build lost on dozor restart, downstream of the
	// debounce fix in dozor_deploy_debounce_persist_total) surfaces as telemetry,
	// not silence. The debounce file only protects a build still within its quiet
	// window; once it FIRES into the queue it lives only in the in-memory pending
	// map + busySHA tracker, which this counter's underlying persistence closes.
	//
	// op label values:
	//   "persist"        — one atomic write of the queue set (pending + in-flight) succeeded (per WRITE, not per entry)
	//   "persist_error"  — an atomic write failed (state file may be stale; build still queued in-memory)
	//   "reload_error"   — boot RecoverQueue could not read or parse the state file (per RELOAD, not per entry);
	//                      EVERY queued/in-flight build it held is lost — the silent-failure hole on the
	//                      recovery path itself, so a non-zero value must alert
	//   "recover"        — a survivor (queued or interrupted-in-flight) was re-enqueued through Submit on boot
	//   "stale_skip"     — a survivor's commit was already the deployed HEAD; no rebuild
	//
	// Label semantics mirror dozor_deploy_debounce_persist_total: "persist",
	// "persist_error" and "reload_error" are per-WHOLE-FILE events with empty
	// repo/service (one write/read covers the whole queue set). "recover" and
	// "stale_skip" are per-ENTRY recovery events and carry real repo/service labels.
	QueuePersistTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_queue_persist_total",
		Help: "Durable build-queue lifecycle events (persist/reload/recover/stale_skip) for restart-survival of queued + in-flight builds.",
	}, []string{"repo", "service", "op"})

	// SkippedTotal counts deploys that were skipped before queueing.
	// `reason` is one of: "skip_if_any", "only_skip_paths",
	// "no_relevant_paths", "no_auto_deploy".
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

	// CrossLaneLockTotal counts cross-lane box-wide ci-lock outcomes for HEAVY
	// builds (P2). outcome label values:
	//   "acquired"          — the shared ci-lock slot was acquired; the heavy build
	//                         is serialised against the CI-runner + other heavy lanes
	//   "timeout_proceeded" — no slot freed within CI_LOCK_WAIT_SECS; the build
	//                         proceeded UNLOCKED (fail-safe, no deadlock). A
	//                         sustained tick here means cross-lane serialisation is
	//                         silently degrading to a no-op — alert on it.
	CrossLaneLockTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_crosslane_lock_total",
		Help: "Cross-lane box-wide build-lock outcomes for heavy builds, by outcome.",
	}, []string{"outcome"})

	// LoadDeferredTotal counts absolute-load backpressure guard outcomes for
	// HEAVY builds (P3). The guard waits for the box 1-minute load average to
	// drop below DOZOR_MAX_LOADAVG (default 2*NumCPU) before a heavy build
	// proceeds, with a fail-safe cap (DOZOR_LOAD_WAIT_SECS, default 600s).
	// outcome label values:
	//   "proceeded_immediately"  — load already below threshold, no wait
	//   "proceeded_after_wait"   — waited, load dropped below threshold, proceeded
	//   "proceeded_timeout"      — waited to the cap (or ctx cancelled), still high, proceeded anyway
	//   "proceeded_read_error"   — couldn't read loadavg, proceeded (fail-open)
	//
	// A sustained tick on "proceeded_timeout" means the box is chronically
	// overloaded — the guard is silently degrading to a no-op. A tick on
	// "proceeded_read_error" on a Linux host means /proc/loadavg is missing
	// (unexpected; investigate). On non-Linux hosts read errors are expected
	// and the guard is a no-op by design.
	LoadDeferredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_build_load_deferred_total",
		Help: "Absolute-load backpressure guard outcomes for heavy builds, by outcome.",
	}, []string{"outcome"})

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

	// ImageCachePushTotal counts image-cache push outcomes (build-once-promote).
	// outcome label values:
	//   "pushed"           — the image was tagged and pushed to the registry successfully
	//   "auth_error"       — pre-push authentication failed (token command error or docker login
	//                        rejected the token); the push was NOT attempted. The classic
	//                        silent-expiry class: the ambient/short-lived credential expired.
	//   "tag_error"        — docker tag (local retag before push) failed
	//   "push_error"       — docker push itself failed (registry down, quota, network, or a
	//                        "denied"-flavoured message after a successful login — which is a
	//                        push-side failure, NOT an auth failure, since login succeeded)
	//   "image_name_error" — the compose-expected image name could not be resolved
	//
	// A non-zero rate on any non-"pushed" outcome means the cache is silently
	// not populating — the pull path will keep falling back to building from
	// source, and the optimisation looks healthy but does nothing. Alert on
	// any non-"pushed" outcome; alert specifically on auth_error for the
	// silent-credential-expiry class (a push_error after a successful login is
	// NOT a credential failure).
	ImageCachePushTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_image_cache_push_total",
		Help: "Image-cache (build-once-promote) push outcomes by repo and outcome.",
	}, []string{"repo", "outcome"})

	// ImageCachePullTotal counts image-cache pull outcomes (build-once-promote).
	// outcome label values:
	//   "reused"     — the image was pulled and retagged; the build was skipped
	//   "miss"       — the image was not in the registry (including a "denied"/"unauthorized"
	//                  message after a successful login — a private registry returns that for
	//                  a non-existent image rather than 404); fell back to building from source
	//   "auth_error" — authentication failed (token command error or docker login rejected
	//                  the token); the pull was NOT attempted. Distinct from "miss" so a
	//                  credential failure is distinguishable from a cold-start cache miss.
	//   "error"      — the retag failed (after a successful pull); fell back to building from source
	//
	// A high "miss" rate with a low "reused" rate means pushes are not landing
	// (cross-reference with ImageCachePushTotal). A "reused" rate near 1.0
	// means the cache is working. A non-zero "auth_error" rate means the
	// registry credential is expiring or wrong — cross-reference with
	// ImageCacheAuthTotal.
	ImageCachePullTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_image_cache_pull_total",
		Help: "Image-cache (build-once-promote) pull outcomes by repo and outcome.",
	}, []string{"repo", "outcome"})

	// ImageCacheAuthTotal counts image-cache registry authentication failures
	// across both the push and the pull path, so a single alertable metric covers
	// the silent-credential-expiry class regardless of which path tripped first.
	//
	// Labels:
	//   repo   — full GitHub repo name
	//   phase  — "push" or "pull" (which path attempted auth)
	//   reason — "token_error"  — the token command failed or returned empty
	//            "login_error"  — docker login rejected the token
	//
	// A non-zero rate on ANY reason means the registry credential is expiring
	// or wrong — the image cache is silently not working. This is the metric
	// that makes the auth_expiry silent-failure class LOUD: a cache that works
	// for one hour after a manual login and then quietly stops is worse than
	// no cache, because nothing said it stopped — this counter says it.
	ImageCacheAuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_image_cache_auth_total",
		Help: "Image-cache registry authentication failures by repo, phase (push|pull), and reason. A non-zero rate means the registry credential is expiring or wrong — the cache is silently not working.",
	}, []string{"repo", "phase", "reason"})

	// ConfigDrift is a state-enum gauge (kube_pod_status_phase style: 1 for
	// the CURRENT outcome, 0 for the others) that makes two classes of silent
	// config drift LOUD without parsing logs:
	//
	//   check="config_mirror" — the live config file (~/.dozor/deploy-repos.yaml)
	//     diverges from its version-controlled mirror (DOZOR_CONFIG_GIT_MIRROR).
	//     Two copies are kept in sync by hand; nothing detects when they diverge.
	//     repo label is empty (a single file comparison). outcome values:
	//       "disabled"          — DOZOR_CONFIG_GIT_MIRROR unset; check skipped (NOT drift)
	//       "mirror_unreadable" — env set but the mirror file is absent/unreadable (NOT drift)
	//       "ok"                — live and mirror are byte-identical
	//       "drift"             — live and mirror differ
	//
	//   check="webhook_events" — a repo's deploy_on requires a GitHub webhook
	//     event that the repo's dozor webhook is not subscribed to. go-hully had
	//     deploy_on: release but its webhook was subscribed to [push] only —
	//     release events never arrived and the repo never deployed, with no
	//     error anywhere, for months. repo label is the repoKey (owner/repo).
	//     outcome values:
	//       "ok"          — webhook events cover what deploy_on needs
	//       "drift"       — webhook exists but is missing a required event
	//       "no_token"    — DOZOR_GITHUB_TOKEN unset; cannot call the API (NOT drift)
	//       "api_error"   — GitHub API call failed (network, non-200, decode) (NOT drift)
	//       "no_webhook"  — no hook on the repo points at dozor's /deploy/github
	//
	// Dashboard "is any repo currently drifted?":
	//   max(dozor_config_drift{outcome="drift"}) > 0
	//
	// The check observes only — it never blocks or fails a deploy.
	ConfigDrift = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dozor_config_drift",
		Help: "Config drift check state by repo and check (1 = current outcome). Dashboard: max(dozor_config_drift{outcome=\"drift\"}) > 0 means a repo is currently drifted.",
	}, []string{"repo", "check", "outcome"})

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
	//   "skipped_diverged"     — local default branch has commits AHEAD of origin (rev-list --count origin/<b>..HEAD > 0); ff refused (benign). NEVER a catch-all for "ff failed".
	//   "skipped_untracked_collision" — clean ancestor (ahead==0, tracked==0) whose ff was blocked by an untracked file shadowing an incoming tracked path; auto-fixable, left untouched when quarantine is off or the retry still failed
	//   "ff_after_quarantine"  — an untracked-collision was auto-resolved: colliding untracked files moved to ~/tmp/git-sync-quarantine and the ff then succeeded (DOZOR_DEPLOY_SOURCE_SYNC_QUARANTINE)
	//   "skipped_quarantine_capped" — >200 untracked colliders; refused bulk-move, escalated to a human
	//   "error"                — a git command failed unexpectedly; checkout left as-is
	//   "panic"                — the sync goroutine panicked and was recovered (must never happen; alert if seen)
	//
	// The sync is best-effort and NEVER touches dozor_build_result_total — that
	// counter's cadence is the control proving the sync is off the critical path.
	DeploySourceSyncTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_deploy_source_sync_total",
		Help: "Best-effort source-checkout (~/src/X) ff-sync attempts after each deploy, by outcome.",
	}, []string{"repo", "result"})

	// ReleaseDiffResolutionTotal counts release-event build-path diff
	// resolution outcomes, making "how often are we rebuilding everything for
	// no reason?" answerable. A sustained tick on any non-"resolved" outcome
	// for a repo means build_paths is silently never applying for that repo's
	// releases — every release rebuilds every service.
	//
	// outcome label values:
	//   "resolved"         — the diff was successfully resolved; build_paths applies
	//   "unresolvable_sha" — the target SHA does not resolve in the source clone
	//                        (configuration error: the clone is not of the webhook's
	//                        repo, or the clone is stale and hasn't fetched the release
	//                        commit). Logged at ERROR naming the clone + SHA.
	//   "no_dir"           — no source dir available (both SourcePath and
	//                        DeployClonePath empty — misconfigured repo)
	//   "no_deployed"      — the deployed SHA could not be resolved (fresh repo,
	//                        never deployed — safe first-build fallback)
	ReleaseDiffResolutionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_release_diff_resolution_total",
		Help: "Release-event build-path diff resolution outcomes by repo. Non-resolved outcomes mean build_paths is not applying — every release rebuilds everything.",
	}, []string{"repo", "outcome"})

	// FetchLockFailOpenTotal counts cases where the per-directory fetch lock
	// (issue #182) could not be set up and the fetch proceeded UNLOCKED —
	// silently degrading back to the exact race the lock exists to prevent.
	// A persistent cause (read-only FS, wrong ownership, full disk) shows up
	// as a sustained tick here with nothing but a WARN log line otherwise.
	// reason label values:
	//   "resolve_failure" — resolveGitCommonDir failed (no .git, broken symlink, etc.)
	//   "open_failure"     — the lock file could not be opened/created (permissions, read-only FS, full disk)
	FetchLockFailOpenTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_fetch_lock_fail_open_total",
		Help: "Fetch-lock infrastructure failures that proceeded unlocked (silent degradation to the #182 race), by reason.",
	}, []string{"reason"})

	// FetchLockTimeoutTotal counts fetch-lock acquisition timeouts — another
	// fetcher held the lock past the wait ceiling, or the caller's context
	// expired before a slot freed. After the FIX 1 sentinel (ErrFetchLock) a
	// timeout is a real error at the call site, not a mislabelled benign skip;
	// this counter is the dedicated contention signal (distinct from a
	// fail-open, which is an infrastructure failure). reason label values:
	//   "deadline" — the lock's own wait deadline fired (another fetcher held it too long)
	//   "context"  — the caller's context expired (ctx.Done, or already-expired on entry)
	FetchLockTimeoutTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_fetch_lock_timeout_total",
		Help: "Fetch-lock acquisition timeouts (contention), by reason. Distinct from fail-open (dozor_fetch_lock_fail_open_total).",
	}, []string{"reason"})

	// PendingDeployGauge is a per-service gauge for "a deployable artifact is
	// ready and has not been deployed yet" — the second half of issue #183.
	// A deploy_on: manual repo builds/pulls its image on the release event but
	// STOPS before composeUp; this gauge is set to 1 for each of its services
	// at that moment and returned to 0 when an explicit server_deploy
	// (ExecuteManualDeploy) brings the containers up.
	//
	// Without it, a fix that is merged, released, and never deployed is
	// invisible: every upstream gate stays green while prod runs stale. The
	// gauge makes that state LOUD — pair it with an alert that fires when it
	// has been 1 for longer than a threshold (the alert text must say what to
	// run: `server_deploy` for the affected repo).
	//
	// Pre-initialised to 0 for every service of every deploy_on: manual repo at
	// startup (PreinitPendingDeployGauge) so "nothing pending" and "the
	// exporter is not running" are distinguishable — an absent series reads as
	// healthy, which is exactly the failure mode this gauge exists to prevent.
	// Labels:
	//   repo    — full GitHub repo name (owner/name)
	//   service — docker compose service name
	PendingDeployGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dozor_pending_deploy",
		Help: "1 when a deployable artifact is ready for a service and has not been deployed (deploy_on: manual). Pre-initialised to 0. Alert when 1 for longer than a threshold — clear by running server_deploy for the repo.",
	}, []string{"repo", "service"})
)

// PreinitPendingDeployGauge pre-initialises dozor_pending_deploy to 0 for every
// service of every deploy_on: manual repo in cfg. Call once at startup, after
// LoadConfig, before serving webhooks. This makes the series EXIST at 0 so a
// dashboard can tell "no pending deploy" (gauge present, value 0) apart from
// "the exporter is not running / the metric vanished" (series absent) — the
// distinction that cost a real fix in a sibling repo. Idempotent and safe to
// call with a config that has no manual repos (no-op).
func PreinitPendingDeployGauge(cfg *Config) {
	if cfg == nil {
		return
	}
	for key, rc := range cfg.Repos {
		if rc.DeployOn != deployOnManual {
			continue
		}
		repo := stripBranchSuffix(key)
		for _, svc := range rc.Services {
			PendingDeployGauge.WithLabelValues(repo, svc).Set(0)
		}
	}
}

// setPendingDeploy sets the dozor_pending_deploy gauge to v for each service
// and mirrors the state to the durable persist file (issue #188) so a
// withheld release survives a dozor restart. repo is the full GitHub repo
// name (owner/name) as carried by BuildRequest.Repo / ManualDeployRequest.Repo.
// The caller MUST guard on deploy_on == manual — this function does not
// re-check, so calling it for a non-manual repo would incorrectly create a
// series for a repo that should never appear on this gauge.
func setPendingDeploy(repo string, services []string, v float64) {
	for _, svc := range services {
		PendingDeployGauge.WithLabelValues(repo, svc).Set(v)
	}
	pendingDeployMu.Lock()
	persistPendingDeployLocked(repo, services, v)
	pendingDeployMu.Unlock()
}
