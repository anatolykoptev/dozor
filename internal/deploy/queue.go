package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	queueSize    = 16
	buildTimeout = 15 * time.Minute
	healthWait   = 30 * time.Second
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

func (q *Queue) executeBuild(ctx context.Context, req BuildRequest) BuildResult {
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	result := BuildResult{
		Repo:     req.Repo,
		Services: req.Config.Services,
	}

	// Step 1: git pull
	if req.Config.SourcePath != "" {
		if err := runCmd(ctx, req.Config.SourcePath,
			"git", "fetch", "origin", "main"); err != nil {
			result.Error = fmt.Sprintf("git fetch: %v", err)
			return result
		}
		if err := runCmd(ctx, req.Config.SourcePath,
			"git", "reset", "--hard", "origin/main"); err != nil {
			result.Error = fmt.Sprintf("git reset: %v", err)
			return result
		}
	}

	// Step 2: docker compose build — snapshot image IDs first so we can detect no-op builds.
	imagesBefore := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)

	buildArgs := []string{"compose", "build"}
	if req.Config.NoCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, req.Config.Services...)

	if err := runCmd(ctx, req.Config.ComposePath,
		"docker", buildArgs...); err != nil {
		result.Error = fmt.Sprintf("docker build: %v", err)
		return result
	}

	imagesAfter := snapshotImages(ctx, req.Config.ComposePath, req.Config.Services)
	logImageDiff(imagesBefore, imagesAfter, req.Config.Services, req.CommitSHA)

	// Step 3: docker compose up
	upArgs := append(
		[]string{"compose", "up", "-d", "--no-deps", "--force-recreate"},
		req.Config.Services...)
	if err := runCmd(ctx, req.Config.ComposePath,
		"docker", upArgs...); err != nil {
		result.Error = fmt.Sprintf("docker up: %v", err)
		return result
	}

	// Step 4: health check (brief wait + verify running)
	time.Sleep(healthWait)
	for _, svc := range req.Config.Services {
		if err := checkHealth(ctx, req.Config.ComposePath, svc); err != nil {
			result.Error = fmt.Sprintf("health check %s: %v", svc, err)
			return result
		}
	}

	// Step 5: smoke test (optional) — fail the deploy if the configured URL doesn't answer 2xx.
	if err := smokeTest(ctx, req.Config.SmokeURL); err != nil {
		result.Error = fmt.Sprintf("smoke test: %v", err)
		return result
	}

	result.Success = true
	return result
}

func runCmd(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, truncate(string(output), maxOutputLen))
	}
	return nil
}

func checkHealth(ctx context.Context, composePath, service string) error {
	cmd := exec.CommandContext(ctx,
		"docker", "compose", "ps", "--format", "{{.Status}}", service)
	cmd.Dir = composePath
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check status: %w", err)
	}
	status := strings.TrimSpace(string(output))
	if !strings.Contains(strings.ToLower(status), "up") {
		return fmt.Errorf("not running: %s", status)
	}
	return nil
}

func serviceKey(services []string) string {
	return strings.Join(services, "+")
}

const (
	maxOutputLen = 500
	shortSHALen  = 7
)

func short(sha string) string {
	if len(sha) > shortSHALen {
		return sha[:shortSHALen]
	}
	return sha
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
