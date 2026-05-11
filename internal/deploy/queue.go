package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
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
	Repo      string
	CommitSHA string
	Config    RepoConfig
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
// Concurrency model:
//   - Single worker goroutine drives all builds; processBuild is synchronous,
//     so two builds can never overlap.
//   - pending[key] holds at most one queued request per service group; arriving
//     requests with a different SHA REPLACE the pending one (no FIFO buildup).
//   - busySHA[key] tracks the SHA currently building, used for SHA-aware dedup
//     ("same commit currently building" → drop, "newer commit" → queue).
//
// A buffered signal channel wakes the worker when new pending work appears; the
// worker drains the entire pending map before sleeping again, so multiple
// service groups can each get their own build in turn (still serialized).
type Queue struct {
	notify func(string)

	mu       sync.Mutex
	pending  map[string]BuildRequest // service-key → next-to-build (newest-wins)
	busySHA  map[string]string       // service-key → SHA currently building (active)
	signal   chan struct{}           // worker wake-up

	// For tests: visibility into the building set without exposing busySHA's SHA.
	building map[string]bool

	cancel context.CancelFunc
	done   chan struct{}
}

// NewQueue creates a build queue and starts the worker goroutine.
func NewQueue(ctx context.Context, notify func(string)) *Queue {
	ctx, cancel := context.WithCancel(ctx)
	q := &Queue{
		notify:   notify,
		pending:  make(map[string]BuildRequest),
		busySHA:  make(map[string]string),
		building: make(map[string]bool),
		signal:   make(chan struct{}, 1),
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go q.worker(ctx)
	return q
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

	// Wake worker. Non-blocking: signal channel is buffered=1, drop dup signals.
	select {
	case q.signal <- struct{}{}:
	default:
	}
	return true
}

// Close shuts down the queue worker.
func (q *Queue) Close() {
	q.cancel()
	<-q.done
}

func (q *Queue) worker(ctx context.Context) {
	defer close(q.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.signal:
			q.drainPending(ctx)
		}
	}
}

// drainPending processes all pending requests, one at a time, until empty.
// Strict global serialization: only one processBuild call active at any time.
// Within the loop, map iteration order is non-deterministic which is fine —
// every pending key gets built before the worker sleeps again.
func (q *Queue) drainPending(ctx context.Context) {
	for {
		// Pick next pending key whose service group is not currently building.
		// (busySHA is set in this same goroutine, so this check is mostly defensive
		// against future concurrency changes; today there is exactly one worker.)
		q.mu.Lock()
		var pickKey string
		var pickReq BuildRequest
		for k, r := range q.pending {
			if _, busy := q.busySHA[k]; busy {
				continue
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
		q.mu.Unlock()

		q.processBuild(ctx, pickReq)

		q.mu.Lock()
		delete(q.busySHA, pickKey)
		delete(q.building, pickKey)
		q.mu.Unlock()
		// Loop continues — if a webhook arrived during the build and replaced
		// q.pending[key] with a newer SHA, it gets picked next iteration. This
		// is the convergence guarantee: after all webhooks settle, the latest
		// SHA per service group is what runs in production.
	}
}

func (q *Queue) processBuild(ctx context.Context, req BuildRequest) {
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
