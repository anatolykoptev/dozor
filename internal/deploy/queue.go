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
	queueSize    = 16
	buildTimeout = 15 * time.Minute
	upMaxRetries = 2
)

// upRetryDelay, healthWait, and portRecoveryWait are variables so tests can override them.
var (
	upRetryDelay     = 10 * time.Second
	healthWait       = 30 * time.Second
	portRecoveryWait = 10 * time.Second
)

// BuildRequest represents a queued build job.
type BuildRequest struct {
	Repo      string
	CommitSHA string
	Config    RepoConfig
}

// BuildResult holds the outcome of a build.
type BuildResult struct {
	Repo     string
	Services []string
	Success  bool
	Duration time.Duration
	Error    string
}

// Queue serializes Docker builds to prevent OOM on ARM.
type Queue struct {
	ch       chan BuildRequest
	notify   func(string)
	mu       sync.Mutex
	queued   map[string]bool // service → already in queue
	building map[string]bool // service → currently building
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewQueue creates a build queue and starts the worker goroutine.
func NewQueue(ctx context.Context, notify func(string)) *Queue {
	ctx, cancel := context.WithCancel(ctx)
	q := &Queue{
		ch:       make(chan BuildRequest, queueSize),
		notify:   notify,
		queued:   make(map[string]bool),
		building: make(map[string]bool),
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go q.worker(ctx)
	return q
}

// Submit adds a build request to the queue.
// Returns false if all services are already queued (deduplicated).
func (q *Queue) Submit(req BuildRequest) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	key := serviceKey(req.Config.Services)
	if q.queued[key] || q.building[key] {
		slog.Info("deploy: deduplicated",
			"services", req.Config.Services,
			"commit", short(req.CommitSHA))
		return false
	}

	q.queued[key] = true

	select {
	case q.ch <- req:
		slog.Info("deploy: queued",
			"services", req.Config.Services,
			"commit", short(req.CommitSHA))
		return true
	default:
		delete(q.queued, key)
		slog.Warn("deploy: queue full, dropping",
			"services", req.Config.Services)
		return false
	}
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
		case req, ok := <-q.ch:
			if !ok {
				return
			}
			q.processBuild(ctx, req)
		}
	}
}

func (q *Queue) processBuild(ctx context.Context, req BuildRequest) {
	key := serviceKey(req.Config.Services)

	q.mu.Lock()
	delete(q.queued, key)
	q.building[key] = true
	q.mu.Unlock()

	defer func() {
		q.mu.Lock()
		delete(q.building, key)
		q.mu.Unlock()
	}()

	services := strings.Join(req.Config.Services, ", ")
	q.notify(fmt.Sprintf(
		"\U0001f528 [%s] Building... (commit %s)", services, short(req.CommitSHA)))

	start := time.Now()
	result := q.executeBuild(ctx, req)
	result.Duration = time.Since(start)

	if result.Success {
		q.notify(fmt.Sprintf(
			"\u2705 [%s] Deployed (%s)", services, result.Duration.Round(time.Second)))
	} else {
		q.notify(fmt.Sprintf(
			"\u274c [%s] FAILED: %s", services, result.Error))
	}
}
