package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// This file makes the in-memory build QUEUE durable across a dozor restart.
//
// Failure class: VOLATILE-PENDING-STATE / lost-on-restart — the SAME class the
// debounce layer fixed one step upstream (debounce_persist.go, PR #110), now at
// the queue layer downstream of it.
//
// The debounce file only protects a build still inside its quiet window. Once
// the window elapses the debouncer FIRES the build through Handler.dispatch →
// Queue.Submit, at which point it leaves the persisted debounce set and lives
// ONLY in two in-memory maps:
//
//   - Queue.pending  — queued, not yet picked by a worker.
//   - Queue.busySHA  — currently building (a `docker compose build` running,
//     up to buildTimeout = 15 min).
//
// On a dozor restart (graceful GHA self-deploy, crash, OOM) both vanish:
//   - a queued-but-not-started build is dropped — the repo never rebuilds.
//   - an in-flight build is INTERRUPTED mid-run — the deploy is half-done and
//     never retried.
//
// Fix (mirrors debounce_persist.go): the authoritative writers of the queue
// state (Submit, drainPending pick, drainPending completion) mirror the live
// set to ~/.dozor/deploy-queue.json via the SAME writeJSONAtomic tmp+rename
// helper. On boot, RecoverQueue reads it back and re-enqueues every survivor
// through the NORMAL Submit path — so recovered builds:
//   - dedup by SHA / service-key (Submit's existing newest-wins + busySHA logic),
//   - serialize through the same heavySem-gated worker path (never N concurrent
//     heavy builds → no OOM on the 4-core ARM host),
//   - skip the rebuild if the persisted commit is already the deployed HEAD
//     (no-stale-rebuild guard, same shaResolver as #110).
//
// In-flight semantics: a build that was busySHA at restart was interrupted, so
// its deploy may be half-applied. We re-enqueue it (an idempotent rebuild +
// `compose up` completes a half-done deploy). This is deliberately at-least-once,
// matching the debounce fire_on_boot choice — a missed deploy is the failure we
// are closing, and a missed deploy is strictly worse than a redundant one.
// The no-stale-rebuild guard skips the re-enqueue when the persisted commit is
// already the deployed HEAD, so a build that actually COMPLETED before the crash
// is not rebuilt. The guard needs a resolvable build dir (DeployClonePath or
// SourcePath); for a config with neither it cannot run, and such a survivor is
// always re-enqueued. That redundant rebuild is NOT free for a heavy repo on the
// 4-core ARM host (a needless `--no-cache` build competes for the memory floor) —
// it is the accepted cost of never silently dropping an interrupted deploy.
//
// No double-recovery with #110: a build mid-debounce is recovered by
// Debouncer.Reload and dispatched into the queue; a build that already reached
// the queue is recovered here. Both arrive via Queue.Submit, which dedups by
// (service-key, SHA) against the in-flight + pending sets — so the same commit
// for the same service group produces exactly ONE build regardless of which
// layer (or both) recovered it. Recovery order at boot is: RecoverQueue first
// (re-enqueues queue survivors, populating pending), THEN Debouncer.Reload
// (fire_on_boot / rearm route through Submit and dedup against that pending set).

// persistedQueueEntry is the durable form of a queued or in-flight BuildRequest.
// inFlight records whether the request was actively building (busySHA) at the
// time of the snapshot — purely informational for the recovery log; recovery
// treats queued and in-flight identically (both re-enqueue through Submit).
//
// NOTE: BuildRequest embeds the full RepoConfig snapshot taken when the build
// was queued. Recovery uses that snapshot (Services for the key, SourcePath /
// DeployClonePath for the stale-skip dir) rather than re-resolving against the
// current deploy-repos.yaml — same trade-off as persistedEntry: every field
// must be JSON-serializable (RepoConfig already round-trips via the debounce
// file), and an operator config edit during downtime makes the recovered build
// use the OLD config. Acceptable for a build that was already in flight.
type persistedQueueEntry struct {
	Request  BuildRequest `json:"request"`
	InFlight bool         `json:"in_flight"`
}

// queuePersistFile is the JSON document written to disk: a flat list of entries.
type queuePersistFile struct {
	Entries []persistedQueueEntry `json:"entries"`
}

// DefaultQueuePersistPath returns ~/.dozor/deploy-queue.json (or
// DOZOR_WORKSPACE/deploy-queue.json), mirroring DefaultDebouncePersistPath so
// the queue state file lives next to deploy-repos.yaml and deploy-debounce.json.
func DefaultQueuePersistPath() string {
	ws := os.Getenv("DOZOR_WORKSPACE")
	if ws == "" {
		home, _ := os.UserHomeDir()
		ws = filepath.Join(home, ".dozor")
	}
	return filepath.Join(ws, "deploy-queue.json")
}

// WithPersistence enables durable mirroring of the queue's pending + in-flight
// set to path and returns the Queue for chaining. A zero path disables
// persistence (the queue behaves exactly as before — in-memory only). The
// shaResolver (for the no-stale-rebuild guard on recovery) defaults to the
// package git resolver; swappable in tests.
func (q *Queue) WithPersistence(path string) *Queue {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.persistPath = path
	if q.shaResolver == nil {
		q.shaResolver = resolveGitSHA
	}
	// Re-arm the drain gate (the constructor closed it for eager draining): with
	// persistence enabled, workers must NOT drain until RecoverQueue has
	// re-enqueued every survivor, so the boot invariant holds against live
	// workers. RecoverQueue closes this gate; if RecoverQueue is never called
	// the queue would never drain, so the gateway MUST call it after this.
	q.drainGate = make(chan struct{})
	return q
}

// persistLocked snapshots the current pending + in-flight set and writes it
// atomically. MUST be called with q.mu held. Best-effort: a write failure is
// logged and counted, never propagated — persistence must not break the queue
// hot path (mirrors Debouncer.persistLocked).
func (q *Queue) persistLocked() {
	if q.persistPath == "" {
		return
	}
	doc := queuePersistFile{Entries: make([]persistedQueueEntry, 0, len(q.pending)+len(q.busyReq))}
	for _, req := range q.busyReq {
		doc.Entries = append(doc.Entries, persistedQueueEntry{Request: req, InFlight: true})
	}
	for _, req := range q.pending {
		doc.Entries = append(doc.Entries, persistedQueueEntry{Request: req, InFlight: false})
	}
	if err := writeJSONAtomic(q.persistPath, doc); err != nil {
		slog.Warn("deploy queue: failed to persist queue state",
			"path", q.persistPath, "error", err)
		QueuePersistTotal.WithLabelValues("", "", "persist_error").Inc()
		return
	}
	// One persist per WRITE with empty repo/service: a persistLocked call
	// rewrites the WHOLE queue set, so a per-repo increment would conflate
	// "a write happened while repo R had an entry" with "repo R was persisted",
	// bumping unrelated repos on every Submit (metrics-convention: the counter
	// must mean exactly its name). Mirrors DebouncePersistTotal "persist".
	QueuePersistTotal.WithLabelValues("", "", "persist").Inc()
}

// RecoverQueue reads the persisted queue set (if any) and re-enqueues each
// survivor through the normal Submit path. Call ONCE at boot, BEFORE the
// debouncer's Reload and before any webhook is served, so that:
//   - queued + interrupted-in-flight builds are restored, and
//   - the restored pending set is in place for Debouncer.Reload to dedup against
//     (no double-recovery — both layers route through Submit's SHA dedup).
//
// A missing or corrupt state file is tolerated (logged + counted + skipped):
// dozor is the deploy orchestrator and must boot even with a damaged state file.
//
// Per entry:
//   - deployed HEAD == persisted commit → stale_skip (no rebuild)
//   - otherwise                         → recover (re-enqueue via Submit; the
//     serial heavySem-gated worker path runs it, never N concurrent heavies)
func (q *Queue) RecoverQueue(ctx context.Context) error {
	// Open the drain gate when recovery finishes (every return path), so workers
	// begin draining the now-fully-populated pending set. WithPersistence re-armed
	// the gate; this is the matching close. Idempotent-guarded so a double call
	// or a path=="" early return cannot panic on a re-close.
	defer q.openDrainGate()

	q.mu.Lock()
	path := q.persistPath
	resolver := q.shaResolver
	q.mu.Unlock()
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // trusted workspace path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Clean boot, nothing queued — not an error.
			return nil
		}
		// Unreadable state file: every build it held is lost. Log AND count —
		// this is the silent-failure hole the fix exists to close, so it must
		// surface as telemetry, not just a log line.
		slog.Warn("deploy queue: cannot read persisted state, starting clean",
			"path", path, "error", err)
		QueuePersistTotal.WithLabelValues("", "", "reload_error").Inc()
		return nil
	}

	var doc queuePersistFile
	if err := json.Unmarshal(data, &doc); err != nil {
		// Corrupt file — do NOT crash the orchestrator. Log, count, drop, continue.
		slog.Warn("deploy queue: persisted state is corrupt, discarding",
			"path", path, "error", err)
		QueuePersistTotal.WithLabelValues("", "", "reload_error").Inc()
		return nil
	}

	recovered := 0
	for _, pe := range doc.Entries {
		recovered += q.recoverQueueEntry(ctx, pe, resolver)
	}
	if recovered > 0 {
		slog.Info("deploy queue: recovered builds after restart",
			"recovered", recovered, "total", len(doc.Entries))
	}
	// Rewrite the file so it reflects the post-recovery set (stale entries
	// dropped, survivors now living as fresh pending). Submit already persisted
	// on each re-enqueue; this final write collapses any stale_skip drops.
	q.mu.Lock()
	q.persistLocked()
	q.mu.Unlock()
	return nil
}

// openDrainGate closes the drain gate so workers may begin draining, unless it
// is already closed (no-persistence path, or a second RecoverQueue call). Held
// under q.mu so the closed-check + close is atomic against a concurrent
// WithPersistence re-arm (which only happens at single-threaded boot, but the
// lock makes the invariant explicit rather than timing-dependent).
func (q *Queue) openDrainGate() {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-q.drainGate:
		// Already open — nothing to do (idempotent).
	default:
		close(q.drainGate)
	}
}

// recoverQueueEntry handles one persisted entry. Returns 1 if it was
// re-enqueued, 0 if skipped (already deployed). Runs without q.mu held —
// Submit takes the lock itself.
func (q *Queue) recoverQueueEntry(ctx context.Context, pe persistedQueueEntry, resolver shaResolverFunc) int {
	req := pe.Request
	svc := serviceKey(req.Config.Services)

	// No-stale-rebuild guard: if the persisted commit already matches the
	// deployed HEAD, the build either already completed or is unnecessary.
	if resolver != nil {
		dir := buildDirForConfig(req.Config)
		if dir != "" {
			deployed := resolver(ctx, dir)
			if deployed != "" && deployed != "unknown" && ShortSHA(deployed) == ShortSHA(req.CommitSHA) {
				slog.Info("deploy queue: skipping recovered build (already deployed)",
					"repo", req.Repo, "service", svc, "commit", short(req.CommitSHA), "in_flight", pe.InFlight)
				QueuePersistTotal.WithLabelValues(req.Repo, svc, "stale_skip").Inc()
				return 0
			}
		}
	}

	// Re-enqueue through the normal Submit path: dedups by (service-key, SHA),
	// serializes through the heavySem-gated worker (no thundering herd), and
	// re-persists. An interrupted in-flight build is re-enqueued identically to
	// a queued one (idempotent rebuild completes a half-done deploy).
	q.Submit(req)
	QueuePersistTotal.WithLabelValues(req.Repo, svc, "recover").Inc()
	slog.Info("deploy queue: re-enqueued recovered build after restart",
		"repo", req.Repo, "service", svc, "commit", short(req.CommitSHA), "in_flight", pe.InFlight)
	return 1
}
