package deploy

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// Durable "last deployed SHA" receipt (issue #174 follow-through). The
// reconciler needs to answer "is the newest release actually deployed?"
// WITHOUT trusting a clone's HEAD — on this fleet the deploy clone is the
// compose repo (krolik-server), and the source clone's HEAD is only advanced
// opportunistically (sync is dirty-skipped / off by default), so neither
// HEAD is a deploy receipt. What IS a receipt: the SHA dozor last built and
// brought up successfully. That is recorded here, keyed by bare owner/repo.
//
// Mirrors pending_deploy_persist.go: JSON state file, read-modify-write under
// pendingDeployMu (shared — both files are written on the same rare paths),
// tmp+rename atomic writes, best-effort (persistence failure logs, never
// propagates).

const deployedSHADefaultFilename = "deployed-sha.json"

// DefaultDeployedSHAPersistPath returns ~/.dozor/deployed-sha.json.
func DefaultDeployedSHAPersistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".dozor", deployedSHADefaultFilename)
}

type deployedSHAFile struct {
	Deployed map[string]string `json:"deployed"` // bare owner/repo → last successfully deployed SHA
}

var (
	// deployedSHAPersistPath is the state file path; empty disables persistence.
	deployedSHAPersistPath string
	// deployedSHAs is the in-memory mirror (map survives even when the file
	// path is unset — the reconcile only needs same-process truth).
	deployedSHAs = map[string]string{}
)

// ConfigureDeployedSHAPersistence sets the state file path and loads any
// existing records. Call once at startup before RecoverQueue and before
// ReconcileMissedReleases.
func ConfigureDeployedSHAPersistence(path string) {
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	deployedSHAPersistPath = path
	deployedSHAs = map[string]string{}
	if path == "" {
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // trusted workspace path
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("deploy: cannot read deployed-SHA state, starting clean",
				"path", path, "error", err)
		}
		return
	}
	var doc deployedSHAFile
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Warn("deploy: persisted deployed-SHA state corrupt, starting clean",
			"path", path, "error", err)
		return
	}
	for repo, sha := range doc.Deployed {
		deployedSHAs[repo] = sha
	}
}

// recordDeployedSHA notes that req's CommitSHA was successfully built and
// deployed for repo. Called from processBuild on Success && !ManualGated and
// from the manual-deploy success path. repo is canonicalised to bare
// owner/repo so every lane (webhook FullName, config key, manual request)
// agrees on one key.
func recordDeployedSHA(repo, sha string) {
	repo = stripBranchSuffix(repo)
	if sha == "" || sha == "unknown" {
		return
	}
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	deployedSHAs[repo] = sha
	if deployedSHAPersistPath == "" {
		return
	}
	doc := deployedSHAFile{Deployed: map[string]string{}}
	if data, err := os.ReadFile(deployedSHAPersistPath); err == nil { //nolint:gosec
		_ = json.Unmarshal(data, &doc)
	}
	if doc.Deployed == nil {
		doc.Deployed = map[string]string{}
	}
	doc.Deployed[repo] = sha
	if err := writeJSONAtomic(deployedSHAPersistPath, doc); err != nil {
		slog.Warn("deploy: failed to persist deployed-SHA state",
			"path", deployedSHAPersistPath, "error", err)
	}
}

// lookupDeployedSHA returns the last recorded deployed SHA for repo (bare or
// suffixed key — canonicalised internally), or "" when none is known.
func lookupDeployedSHA(repo string) string {
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	return deployedSHAs[stripBranchSuffix(repo)]
}

// RecordManualDeployReceipt records the receipt for a successful
// server_deploy (internal/engine's manual-deploy path). An on_demand lane is
// skipped: the receipt is keyed by bare owner/repo, so it belongs to the
// repo's automatic lane, and a canary deployed at the branch tip would make
// the boot reconciler see production "behind its tag" and rebuild it.
func RecordManualDeployReceipt(req ManualDeployRequest, sha string) {
	if req.Config.DeployOn == deployOnOnDemand {
		return
	}
	recordDeployedSHA(req.Repo, sha)
}
