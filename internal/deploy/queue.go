package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
)

const (
	buildTimeout = 15 * time.Minute
	upMaxRetries = 2
)

// upRetryDelay, healthWait, portRecoveryWait, maintenancePollInterval, and maintenanceMaxWait
// are variables so tests can override them.
var (
	upRetryDelay            = 10 * time.Second
	healthWait              = 30 * time.Second
	portRecoveryWait        = 10 * time.Second
	maintenancePollInterval = 10 * time.Second
	maintenanceMaxWait      = 5 * time.Minute
)

// BuildRequest represents a queued build job.
type BuildRequest struct {
	Repo         string
	CommitSHA    string
	ChangedPaths []string // union of changed file paths across all commits in the push; nil = unknown (force-push or oversized)
	Config       RepoConfig
}

// BuildResult holds the outcome of a build.
type BuildResult struct {
	Repo           string
	Services       []string
	Success        bool
	Duration       time.Duration
	Error          string
	RolledBack     bool              // true if rollback was attempted and succeeded
	PreviousImages map[string]string // service → image ID before compose up
}

// Queue serializes Docker builds: at most ONE build runs at a time across all
// service groups, preventing OOM on the ARM build host. Per service-group, the
// next pending request is coalesced to newest-wins by SHA — multiple webhooks
// arriving while a build is in flight collapse into a single follow-up build
// of the latest commit.
//
// Queue manages Docker builds with configurable concurrency.
//
// Concurrency model:
//   - N worker goroutines drive builds concurrently (N = DOZOR_BUILD_CONCURRENCY,
//     default 1 for backward compat). Each worker calls drainPending independently.
//   - "Heavy" repos (heavy: true in dozor.yml) additionally acquire heavySem(1),
//     so at most one heavy build runs regardless of N â preventing OOM on ARM.
//   - pending[key] holds at most one queued request per service group (newest-wins).
//   - busySHA[key] tracks the SHA currently building, used for SHA-aware dedup.
//   - signal is buffered N; after picking work a worker re-signals if pending is
//     non-empty, ensuring all workers stay busy when work is available.
type Queue struct {
	notify func(string)

	mu       sync.Mutex
	pending  map[string]BuildRequest // service-key â next-to-build (newest-wins)
	busySHA  map[string]string       // service-key â SHA currently building (active)
	signal   chan struct{}           // worker wake-up (buffered N)

	// For tests: visibility into the building set without exposing busySHA's SHA.
	building map[string]bool

	// heavySem gates heavy builds: at most one heavy build runs at a time,
	// regardless of DOZOR_BUILD_CONCURRENCY, to prevent OOM on the ARM host.
	// Light builds skip this semaphore entirely.
	heavySem *semaphore.Weighted

	cancel context.CancelFunc
	done   chan struct{} // closed when all workers have exited (compat with newStoppedQueue)
	wg     sync.WaitGroup // tracks all worker goroutines
}

// NewQueue creates a build queue with concurrency=1 (backward-compatible default).
// Use NewQueueN for a higher concurrency limit.
func NewQueue(ctx context.Context, notify func(string)) *Queue {
	return NewQueueN(ctx, notify, 1)
}

// NewQueueN creates a build queue and starts n worker goroutines.
// n <= 0 is clamped to 1.
//
// Env knob (resolved by the caller, not here): DOZOR_BUILD_CONCURRENCY.
// Default 1 = identical to the old single-worker behaviour.
func NewQueueN(ctx context.Context, notify func(string), n int) *Queue {
	if n <= 0 {
		n = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	q := &Queue{
		notify:   notify,
		pending:  make(map[string]BuildRequest),
		busySHA:  make(map[string]string),
		building: make(map[string]bool),
		signal:   make(chan struct{}, n), // buffered N so all workers can be woken
		heavySem: semaphore.NewWeighted(1),
		cancel:   cancel,
		done:     make(chan struct{}), // closed when all workers have exited
	}
	q.wg.Add(n)
	for range n {
		go q.worker(ctx)
	}
	// Close done when all workers exit.
	go func() { q.wg.Wait(); close(q.done) }()
	// Publish to the activeQueue singleton so external read-only callers can
	// find us without explicit plumbing. Last-writer-wins if multiple queues
	// are constructed in the same process (only tests do this).
	activeQueue.Store(q)
	return q
}

// IsActiveOrPending reports whether the given service-key has a build currently
// in flight or queued to run next. Used by dispatchPush to bypass the debounce
// window when the queue already has work for this service group â the queue's
// own newest-wins dedup handles the second webhook correctly, eliminating the
// 30-60 s debounce latency in the bursty case.
//
// key must be serviceKey(rc.Services) â no repo-name prefix.
func (q *Queue) IsActiveOrPending(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, busy := q.busySHA[key]
	_, pend := q.pending[key]
	return busy || pend
}

// queued is a test helper / back-compat shim: returns whether a key has a
// pending build waiting. Mirrors the prior public-shaped behaviour but is
// derived from the new pending map. Read under q.mu.
func (q *Queue) queuedHas(key string) bool {
	_, ok := q.pending[key]
	return ok
}

// Submit enqueues a build request with newest-wins coalescing.
//
// Behaviour matrix:
//
//	currently building same SHA → DEDUP (true exact-duplicate webhook)
//	pending same SHA → DEDUP (already queued same commit)
//	currently building different SHA → ENQUEUE / REPLACE pending (newest-wins)
//	pending different SHA → REPLACE pending with newer (log "superseded")
//	idle → ENQUEUE
//
// Returns true if the request was enqueued/replaced as the next build for
// its service group; false if it was deduplicated against an in-flight or
// pending build of the same exact SHA.
func (q *Queue) Submit(req BuildRequest) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	key := serviceKey(req.Config.Services)

	// Currently building the same SHA → real duplicate (e.g. GitHub webhook retry).
	if currentSHA, busy := q.busySHA[key]; busy && currentSHA == req.CommitSHA {
		slog.Info("deploy: deduplicated (currently building same SHA)",
			"services", req.Config.Services,
			"commit", short(req.CommitSHA))
		for _, svc := range req.Config.Services {
			DeduplicatedTotal.WithLabelValues(req.Repo, svc).Inc()
		}
		return false
	}

	// Already pending the same SHA → real duplicate.
	if existing, has := q.pending[key]; has && existing.CommitSHA == req.CommitSHA {
		slog.Info("deploy: deduplicated (already pending same SHA)",
			"services", req.Config.Services,
			"commit", short(req.CommitSHA))
		for _, svc := range req.Config.Services {
			DeduplicatedTotal.WithLabelValues(req.Repo, svc).Inc()
		}
		return false
	}

	// Newest-wins replacement: if pending has a different (older) SHA, replace it.
	// If currently building a different SHA, enqueue this as next.
	if existing, has := q.pending[key]; has {
		slog.Info("deploy: pending superseded by newer commit",
			"services", req.Config.Services,
			"old_sha", short(existing.CommitSHA),
			"new_sha", short(req.CommitSHA))
		for _, svc := range req.Config.Services {
			SupersededTotal.WithLabelValues(req.Repo, svc).Inc()
		}
	}
	q.pending[key] = req
	slog.Info("deploy: queued",
		"services", req.Config.Services,
		"commit", short(req.CommitSHA))

	// Wake worker. Non-blocking: signal channel is buffered N (DOZOR_BUILD_CONCURRENCY), drop dup signals.
	select {
	case q.signal <- struct{}{}:
	default:
	}
	return true
}

// Close shuts down all queue workers and waits for them to exit.
func (q *Queue) Close() {
	q.cancel()
	// done is closed by the watcher goroutine once all workers have exited.
	<-q.done
	// Clear the active-queue pointer only if we are still the active one.
	// Lets tests stand up a Queue and tear it down without poisoning the
	// process-wide pointer for a subsequent test.
	if activeQueue.Load() == q {
		activeQueue.Store(nil)
	}
}

// ── Active-queue singleton ────────────────────────────────────────────────────

// activeQueue holds the currently-running Queue so external read-only callers
// (e.g. internal/tools/server_deploy_check) can inspect snapshot state without
// being plumbed an explicit Queue handle. NewQueue registers; Close clears.
//
// This is a late-binding singleton: tools register at process start (before
// the queue exists) and look up the queue lazily. Idiomatic Go pattern —
// mirrors prometheus.DefaultGatherer.
var activeQueue atomic.Pointer[Queue]

// ActiveQueue returns the currently-registered Queue, or nil if no queue has
// been created yet (e.g. during early MCP-tool registration in gateway init).
func ActiveQueue() *Queue { return activeQueue.Load() }

// ServiceQueueState is the inspectable per-service-group snapshot returned by
// Queue.Snapshot. Designed for human-readable output, not metrics scraping.
type ServiceQueueState struct {
	// Services is the list of compose / user-service names this entry covers
	// (the queue keys builds by joined service group).
	Services []string
	// BuildingSHA is the commit currently being built for this group, or "" if idle.
	BuildingSHA string
	// PendingSHA is the next commit queued to build, or "" if nothing pending.
	PendingSHA string
}

// Snapshot returns a point-in-time view of every service group with either a
// build in progress, a pending build, or both. Idle groups are omitted. Read
// under the queue mutex; the returned slice is independent of the live maps.
func (q *Queue) Snapshot() []ServiceQueueState {
	q.mu.Lock()
	defer q.mu.Unlock()

	keys := make(map[string]struct{})
	for k := range q.pending {
		keys[k] = struct{}{}
	}
	for k := range q.busySHA {
		keys[k] = struct{}{}
	}
	if len(keys) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	out := make([]ServiceQueueState, 0, len(ordered))
	for _, k := range ordered {
		state := ServiceQueueState{Services: strings.Split(k, "+")}
		if sha, ok := q.busySHA[k]; ok {
			state.BuildingSHA = sha
		}
		if req, ok := q.pending[k]; ok {
			state.PendingSHA = req.CommitSHA
		}
		out = append(out, state)
	}
	return out
}

func (q *Queue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.signal:
			q.drainPending(ctx)
		}
	}
}

// drainPending picks one ready pending request and executes it. Multiple workers
// call this concurrently; the mutex ensures each picks a distinct key.
//
// Heavy builds use TryAcquire on heavySem (non-blocking): if another heavy
// build is running the key is skipped. The releaser re-signals so the skipped
// heavy build is retried once the semaphore is free.
func (q *Queue) drainPending(ctx context.Context) {
	for {
		q.mu.Lock()
		var pickKey string
		var pickReq BuildRequest
		var isHeavy bool
		for k, r := range q.pending {
			if _, busy := q.busySHA[k]; busy {
				continue
			}
			if r.Config.Heavy {
				// Non-blocking: skip if another heavy build holds the semaphore.
				if !q.heavySem.TryAcquire(1) {
					continue
				}
				isHeavy = true
			}
			pickKey = k
			pickReq = r
			break
		}
		if pickKey == "" {
			q.mu.Unlock()
			return
		}
		delete(q.pending, pickKey)
		q.busySHA[pickKey] = pickReq.CommitSHA
		q.building[pickKey] = true
		// Re-signal if more pending work exists so another worker can pick it up.
		if len(q.pending) > 0 {
			select {
			case q.signal <- struct{}{}:
			default:
			}
		}
		q.mu.Unlock()

		q.processBuild(ctx, pickReq, isHeavy)

		q.mu.Lock()
		delete(q.busySHA, pickKey)
		delete(q.building, pickKey)
		q.mu.Unlock()
		if isHeavy {
			q.heavySem.Release(1)
			// Wake a worker that skipped this key while the semaphore was held.
			select {
			case q.signal <- struct{}{}:
			default:
			}
		}
		// Loop: pick next pending for this worker (newest-wins convergence).
	}
}

func (q *Queue) processBuild(ctx context.Context, req BuildRequest, isHeavy bool) {
	class := "light"
	if isHeavy {
		class = "heavy"
	}
	BuildInflight.WithLabelValues(class).Inc()
	defer BuildInflight.WithLabelValues(class).Dec()

	services := strings.Join(req.Config.Services, ", ")
	q.notify(fmt.Sprintf(
		"\U0001f528 [%s] Building... (commit %s)", services, short(req.CommitSHA)))

	start := time.Now()
	result := q.executeBuild(ctx, req)
	result.Duration = time.Since(start)

	// Record build result metrics for each service.
	status := "success"
	if !result.Success {
		// Distinguish timeout from other failures for better observability.
		effectiveTimeout := req.Config.BuildTimeout.OrDefault(buildTimeout)
		if strings.Contains(strings.ToLower(result.Error), "context deadline exceeded") ||
			strings.Contains(strings.ToLower(result.Error), "timeout") ||
			result.Duration >= effectiveTimeout {
			status = "timeout"
		} else {
			status = "failure"
		}
	}
	for _, svc := range req.Config.Services {
		BuildResultTotal.WithLabelValues(req.Repo, svc, status).Inc()
	}

	// Best-effort source-checkout sync, OFF the critical path: advance this
	// repo's ~/src/X default-branch ref to origin so go-code indexes fresh and
	// the dev checkout stays current. Runs UNCONDITIONALLY (success OR failure —
	// a failed build can still have merged new commits to origin), in a detached
	// goroutine with its own timeout + recover, so a sync hang/panic/error can
	// never block, delay, or fail the deploy or touch BuildResult. Default OFF
	// behind DOZOR_DEPLOY_SOURCE_SYNC (the flag check inside syncSourceCheckout
	// records "skipped_disabled" — we always launch so the disabled path is
	// observable).
	if req.Config.SourcePath != "" {
		// G118: context.Background() is DELIBERATE here, not the deploy ctx — the
		// sync must outlive a cancelled deploy and own its independent 60s timeout
		// so it can never block the worker (architect Decision 4, failure isolation).
		//nolint:gosec // G118: independent timeout context is the isolation requirement
		go func(repo, src, clone string) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("deploy: source sync goroutine panicked", "repo", repo, "source", src, "panic", r)
					DeploySourceSyncTotal.WithLabelValues(repo, "panic").Inc()
				}
			}()
			sctx, cancel := context.WithTimeout(context.Background(), sourceSyncTimeout)
			defer cancel()
			res := syncSourceCheckout(sctx, repo, src, clone)
			DeploySourceSyncTotal.WithLabelValues(repo, string(res)).Inc()
		}(req.Repo, req.Config.SourcePath, req.Config.DeployClonePath)
	}

	if result.Success {
		q.notify(fmt.Sprintf(
			"✅ [%s] Deployed (%s)", services, result.Duration.Round(time.Second)))
	} else if result.RolledBack {
		q.notify(fmt.Sprintf(
			"⚠️ [%s] FAILED (rolled back): %s", services, result.Error))
	} else {
		q.notify(fmt.Sprintf(
			"❌ [%s] FAILED: %s", services, result.Error))
	}
}
