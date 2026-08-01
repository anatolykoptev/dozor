package deploy

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// This file makes the dozor_pending_deploy gauge durable across a dozor
// restart — issue #188. The gauge lives in process memory and
// PreinitPendingDeployGauge resets every deploy_on: manual repo to 0 at
// startup, so a release withheld before a restart reads as "nothing pending"
// afterwards. That is worse than absent: the series still exists and reads a
// confident 0 while production is behind.
//
// Direction choice (issue #188 names two): PERSIST, not derive.
//
// Derivation (compare latest release tag vs what is running) was the
// preferred direction but is impractical for 3 of 4 kind/config
// combinations:
//   - Binary/static: the deploy is git pull + build + systemctl restart
//     (manual_deploy.go:254-310). There is no artifact tag, no registry, no
//     content address, and no version probe of the running process. Deriving
//     "is the running binary behind the latest release?" would require a
//     per-service version endpoint that does not exist.
//   - Compose WITHOUT image-cache: the image is <project>-<svc>:latest,
//     rebuilt in place — no tree-hash tag to compare against
//     (image_cache.go:189-193 — tryPullCachedImage returns false when
//     cacheableServices is empty). Only compose WITH image-cache has a
//     content-address (<registry>:tree-<hash>), and even there derivation
//     needs a new "read the running container's image TAG (not ID)" step
//     that does not exist (composeImageID reads the image SHA, not the tag).
//
// The issue's staleness concern (a deploy while dozor is down leaves a
// persisted file lying) is structurally impossible for the automated path:
// ExecuteManualDeploy — the ONLY gauge-clearer — is reachable solely via
// StartManualDeploy ← HandleDeploy, the server_deploy MCP tool dozor itself
// serves (vaelor call_trace: production_caller_count=1). If dozor is down,
// server_deploy is unreachable, so no deploy can clear the pending state.
// The only staleness vector is a manual operator deploy (ssh + docker compose
// up by hand) while dozor is down — rare, operator-visible, and self-
// correcting on the next release cycle.
//
// Mechanism (mirrors queue_persist.go / debounce_persist.go): setPendingDeploy
// mirrors the gauge state to ~/.dozor/deploy-pending.json via the shared
// writeJSONAtomic tmp+rename helper. On boot, PreinitPendingDeployGauge seeds
// every manual repo at 0, THEN RestorePendingDeployGauge reads the file and
// sets 1 for every (repo, service) that was pending at shutdown.

// pendingDeployPersistPath is the file path for durable pending-deploy state.
// Empty (default) = persistence disabled (gauge is in-memory only, pre-#188
// behaviour). Set once at startup via ConfigurePendingDeployPersistence.
var pendingDeployPersistPath string

// pendingDeployMu guards pendingDeployPersistPath and the read-modify-write
// cycle on the persist file. setPendingDeploy is called from queue workers
// and the MCP handler goroutine; without this lock two concurrent
// setPendingDeploy calls could race the read-modify-write and lose an entry.
var pendingDeployMu sync.Mutex

// pendingDeployFile is the JSON document written to disk: repo → list of
// services that are pending (gauge=1). Absent repo = not pending.
type pendingDeployFile struct {
	Pending map[string][]string `json:"pending"`
}

// DefaultPendingDeployPersistPath returns ~/.dozor/deploy-pending.json (or
// DOZOR_WORKSPACE/deploy-pending.json), mirroring DefaultDebouncePersistPath
// and DefaultQueuePersistPath so the state file lives next to the other
// deploy state files.
func DefaultPendingDeployPersistPath() string {
	ws := os.Getenv("DOZOR_WORKSPACE")
	if ws == "" {
		home, _ := os.UserHomeDir()
		ws = filepath.Join(home, ".dozor")
	}
	return filepath.Join(ws, "deploy-pending.json")
}

// ConfigurePendingDeployPersistence sets the path for durable pending-deploy
// state. Call once at startup, before any webhook is served (and before
// queue.RecoverQueue, which may re-enqueue a gated build that calls
// setPendingDeploy). A zero path disables persistence (the gauge is
// in-memory only — pre-#188 behaviour).
func ConfigurePendingDeployPersistence(path string) {
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	pendingDeployPersistPath = path
}

// persistPendingDeployLocked updates the durable pending-deploy state file:
// sets the repo's services when v=1, removes the repo entry when v=0. MUST
// be called with pendingDeployMu held. Best-effort: a write failure is
// logged, never propagated — persistence must not break the deploy hot path
// (mirrors queue_persist.go and debounce_persist.go).
func persistPendingDeployLocked(repo string, services []string, v float64) {
	if pendingDeployPersistPath == "" {
		return
	}
	doc := readPendingDeployFile()
	if doc.Pending == nil {
		doc.Pending = make(map[string][]string)
	}
	if v == 1 {
		doc.Pending[repo] = services
	} else {
		delete(doc.Pending, repo)
	}
	if err := writeJSONAtomic(pendingDeployPersistPath, doc); err != nil {
		slog.Warn("deploy: failed to persist pending-deploy state",
			"path", pendingDeployPersistPath, "error", err)
	}
}

// readPendingDeployFile reads and parses the persist file. Returns an empty
// doc on missing file (clean boot), corrupt file (logged + discarded), or
// when persistence is disabled. MUST be called with pendingDeployMu held.
func readPendingDeployFile() pendingDeployFile {
	var doc pendingDeployFile
	if pendingDeployPersistPath == "" {
		return doc
	}
	data, err := os.ReadFile(pendingDeployPersistPath) //nolint:gosec // trusted workspace path
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("deploy: cannot read pending-deploy state, starting clean",
				"path", pendingDeployPersistPath, "error", err)
		}
		return doc
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Warn("deploy: persisted pending-deploy state is corrupt, discarding",
			"path", pendingDeployPersistPath, "error", err)
		return pendingDeployFile{}
	}
	return doc
}

// RestorePendingDeployGauge reads the persisted pending-deploy state and
// restores the gauge to 1 for every (repo, service) that was pending at the
// time of the last shutdown. Call once at startup, AFTER
// PreinitPendingDeployGauge (which seeds every manual repo at 0), so the
// restore overrides the 0 for repos that were actually pending.
//
// Tolerant of a missing or corrupt state file (logs + continues): dozor is
// the deploy orchestrator and must boot even with a damaged state file.
func RestorePendingDeployGauge() {
	pendingDeployMu.Lock()
	path := pendingDeployPersistPath
	pendingDeployMu.Unlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // trusted workspace path
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("deploy: cannot read persisted pending-deploy state, starting clean",
				"path", path, "error", err)
		}
		return
	}
	var doc pendingDeployFile
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Warn("deploy: persisted pending-deploy state is corrupt, discarding",
			"path", path, "error", err)
		return
	}
	for repo, services := range doc.Pending {
		for _, svc := range services {
			PendingDeployGauge.WithLabelValues(repo, svc).Set(1)
		}
		slog.Info("deploy: restored pending-deploy gauge after restart",
			"repo", repo, "services", services)
	}
}
