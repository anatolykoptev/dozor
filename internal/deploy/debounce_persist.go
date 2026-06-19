package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// This file makes the in-memory debounce queue durable across a dozor restart.
//
// Failure class: VOLATILE-PENDING-STATE / lost-timer-on-restart.
// A debounced build lived only as an in-memory time.Timer + goroutine + map
// entry. On graceful restart (every dozor self-deploy calls Handler.Close →
// Debouncer.Stop, which aborts the wait WITHOUT firing) or on crash/OOM the
// queued build vanished and the repo never rebuilt for that push.
//
// Fix: the single authoritative writer of Debouncer.pending (Submit /
// waitAndFire / Stop) now mirrors the live set to a small JSON file via an
// atomic tmp+rename write. On boot, Reload reads it back and routes every
// surviving entry through the normal dispatch path (the serial build queue),
// re-arming a timer for the remaining window or firing immediately if the
// absolute deadline already elapsed during downtime. Entries whose commit is
// already the deployed HEAD are skipped (no stale rebuild).

// persistedEntry is the durable form of a pendingEntry. It stores the event
// plus the ABSOLUTE fire deadline (LastSeen + window). Deadline-based — not
// remaining-time-based — so a reload after a long downtime computes the
// correct (possibly already-elapsed) remaining window instead of wrongly
// re-arming a full window.
// NOTE: Event embeds the full RepoConfig snapshot taken when the webhook
// arrived. Recovery uses that snapshot (DeployClonePath/SourcePath for the
// stale-skip dir, Services for dispatch) rather than re-resolving against the
// current deploy-repos.yaml. Two consequences worth knowing: (1) any RepoConfig
// field must be JSON-serializable to survive the round-trip; (2) if an operator
// edits a repo's config and restarts dozor while a build is queued, the
// recovered build uses the OLD config. Both are acceptable for a debounce
// window (seconds-to-minutes) and avoid coupling recovery to live config
// lookup. Revisit if config drift across a restart becomes a real problem.
type persistedEntry struct {
	Key      string       `json:"key"`
	Event    PendingEvent `json:"event"`
	Deadline time.Time    `json:"deadline"`
}

// persistFile is the JSON document written to disk: a flat list of entries.
type persistFile struct {
	Entries []persistedEntry `json:"entries"`
}

// DefaultDebouncePersistPath returns ~/.dozor/deploy-debounce.json (or
// DOZOR_WORKSPACE/deploy-debounce.json), mirroring DefaultConfigPath so the
// state file lives next to deploy-repos.yaml.
func DefaultDebouncePersistPath() string {
	ws := os.Getenv("DOZOR_WORKSPACE")
	if ws == "" {
		home, _ := os.UserHomeDir()
		ws = filepath.Join(home, ".dozor")
	}
	return filepath.Join(ws, "deploy-debounce.json")
}

// shaResolverFunc resolves the currently-deployed short SHA for a repo, given
// the directory dozor would build from. Defaults to the package git resolver;
// swappable in tests.
type shaResolverFunc func(ctx context.Context, dir string) string

// WithPersistence enables durable mirroring of the pending set to path and
// returns the Debouncer for chaining. A zero path disables persistence (the
// debouncer behaves exactly as before — in-memory only).
func (d *Debouncer) WithPersistence(path string) *Debouncer {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.persistPath = path
	if d.shaResolver == nil {
		d.shaResolver = resolveGitSHA
	}
	return d
}

// persistLocked snapshots the current pending set and writes it atomically.
// MUST be called with d.mu held. Best-effort: a write failure is logged and
// counted, never propagated — persistence must not break the hot path.
func (d *Debouncer) persistLocked() {
	if d.persistPath == "" {
		return
	}
	doc := persistFile{Entries: make([]persistedEntry, 0, len(d.pending))}
	for key, entry := range d.pending {
		doc.Entries = append(doc.Entries, persistedEntry{
			Key:      key,
			Event:    entry.event,
			Deadline: entry.event.LastSeen.Add(entry.window),
		})
	}
	if err := writeJSONAtomic(d.persistPath, doc); err != nil {
		slog.Warn("deploy debounce: failed to persist pending state",
			"path", d.persistPath, "error", err)
		DebouncePersistTotal.WithLabelValues("", "", "persist_error").Inc()
		return
	}
	// Count ONE persist per write, with empty repo/service (mirrors
	// persist_error). A persistLocked call rewrites the WHOLE pending set, so a
	// per-entry increment would conflate "a write happened while repo R had a
	// pending entry" with "repo R's entry was persisted" — bumping unrelated
	// repos' counters on every Submit. (metrics-convention: a counter must mean
	// exactly what its name says; per-repo persist volume is not measurable here
	// without double-counting.)
	DebouncePersistTotal.WithLabelValues("", "", "persist").Inc()
}

// writeJSONAtomic marshals v and writes it to path via a temp file + rename so
// a concurrent reader (or a crash mid-write) never sees a truncated document.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:mnd // standard workspace dir mode
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".deploy-debounce-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck // already returning the write error
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp into place: %w", err)
	}
	return nil
}

// Reload reads the persisted pending set (if any) and recovers each surviving
// entry through the normal dispatch path. It is safe to call once at boot,
// before the handler serves any webhook. A missing or corrupt state file is
// tolerated (logged + skipped) — dozor is the deploy orchestrator and must
// boot even with a damaged state file.
//
// Per entry:
//   - deployed HEAD == persisted commit  → stale_skip (no rebuild)
//   - deadline already elapsed           → fire_on_boot (dispatch now, serialized by the queue)
//   - deadline in the future             → rearm (fresh timer for the remaining window)
func (d *Debouncer) Reload(ctx context.Context) error {
	d.mu.Lock()
	path := d.persistPath
	d.mu.Unlock()
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // trusted workspace path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Clean boot, nothing pending — not an error.
			return nil
		}
		// Unreadable state file (not a clean "no file"): every build it held is
		// lost. Log AND count — this is the silent-failure hole the whole fix
		// exists to close, so it must surface as telemetry, not just a log line.
		slog.Warn("deploy debounce: cannot read persisted state, starting clean",
			"path", path, "error", err)
		DebouncePersistTotal.WithLabelValues("", "", "reload_error").Inc()
		return nil
	}

	var doc persistFile
	if err := json.Unmarshal(data, &doc); err != nil {
		// Corrupt file — do NOT crash the orchestrator. Log, count, drop, continue.
		// (Same recovery-path silent-failure hole as the read-error branch above.)
		slog.Warn("deploy debounce: persisted state is corrupt, discarding",
			"path", path, "error", err)
		DebouncePersistTotal.WithLabelValues("", "", "reload_error").Inc()
		return nil
	}

	now := d.clock.Now()
	recovered := 0
	for _, pe := range doc.Entries {
		recovered += d.recoverEntry(ctx, pe, now)
	}
	if recovered > 0 {
		slog.Info("deploy debounce: recovered pending builds after restart",
			"recovered", recovered, "total", len(doc.Entries))
	}
	// Rewrite the file so it reflects the post-reload set (stale/fired entries
	// dropped). Holds the lock to stay consistent with concurrent Submit.
	d.mu.Lock()
	d.persistLocked()
	d.mu.Unlock()
	return nil
}

// recoverEntry handles one persisted entry. Returns 1 if it was re-armed or
// fired, 0 if skipped. Runs without d.mu held except where it mutates pending.
func (d *Debouncer) recoverEntry(ctx context.Context, pe persistedEntry, now time.Time) int {
	ev := pe.Event

	// No-stale-rebuild guard: if the persisted commit already matches the
	// deployed HEAD, the build either already happened or is unnecessary.
	if d.shaResolver != nil {
		dir := buildDirForConfig(ev.Config)
		if dir != "" {
			deployed := d.shaResolver(ctx, dir)
			if deployed != "" && deployed != "unknown" && ShortSHA(deployed) == ShortSHA(ev.CommitSHA) {
				slog.Info("deploy debounce: skipping recovered build (already deployed)",
					"repo", ev.Repo, "service", ev.Service, "commit", short(ev.CommitSHA))
				DebouncePersistTotal.WithLabelValues(ev.Repo, ev.Service, "stale_skip").Inc()
				return 0
			}
		}
	}

	remaining := pe.Deadline.Sub(now)
	if remaining <= 0 {
		// Deadline elapsed during downtime → fire immediately, but still route
		// through dispatch (the serial queue) — never build directly here.
		DebouncePersistTotal.WithLabelValues(ev.Repo, ev.Service, "fire_on_boot").Inc()
		slog.Info("deploy debounce: firing recovered build on boot (deadline elapsed)",
			"repo", ev.Repo, "service", ev.Service, "commit", short(ev.CommitSHA))
		go d.dispatch(ev)
		return 1
	}

	// Re-arm a fresh timer for the REMAINING window only. The stored window is
	// the original full window (Deadline − LastSeen) so that if a new webhook
	// coalesces onto this entry post-reload, Submit re-persists the correct
	// (LastSeen + window) deadline.
	cancel := make(chan struct{})
	entry := &pendingEntry{
		event:  ev,
		timer:  d.clock.NewTimer(remaining),
		cancel: cancel,
		window: pe.Deadline.Sub(ev.LastSeen),
	}
	d.mu.Lock()
	d.pending[pe.Key] = entry
	d.mu.Unlock()
	DebouncePersistTotal.WithLabelValues(ev.Repo, ev.Service, "rearm").Inc()
	slog.Info("deploy debounce: re-armed recovered build after restart",
		"repo", ev.Repo, "service", ev.Service, "commit", short(ev.CommitSHA),
		"remaining", remaining.String())
	go d.waitAndFire(pe.Key, entry)
	return 1
}

// buildDirForConfig returns the directory whose HEAD represents the deployed
// commit for stale comparison: the deploy clone if configured, else the
// source checkout. Empty when neither is known (stale-skip is then bypassed).
func buildDirForConfig(rc RepoConfig) string {
	if rc.DeployClonePath != "" {
		return rc.DeployClonePath
	}
	return rc.SourcePath
}
