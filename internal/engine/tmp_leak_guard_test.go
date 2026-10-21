package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/anatolykoptev/dozor/internal/testutil/tempguard"
)

// The guard sandboxes TMPDIR for the whole suite and fails the run if a
// deploy-owned temp entry (deploy-manual-*.log et al.) is created outside
// it — the /tmp-leak sentinel for the engine package.
func TestMain(m *testing.M) {
	os.Exit(tempguard.RunWithGuard(m))
}

// The deploy-manual-<ts>.log outcome file is intentionally kept — its path is
// advertised in the server_deploy reply and read back by deploy-status — but
// the family must stay bounded: StartManualDeploy prunes it to the newest
// deploy.MaxDeployTempLogs at create time.
//
// RED-on-revert: remove the deploy.PruneTempLogs call in StartManualDeploy —
// the seeded files are never pruned and the count assertion fails.
func TestStartManualDeploy_PrunesOldDeployLogs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	// Seed more deploy-manual-*.log files than the bound allows, with mtimes
	// far in the past so the prune sees them as the oldest.
	for i := 0; i < deploy.MaxDeployTempLogs+2; i++ {
		name := filepath.Join(dir, fmt.Sprintf("deploy-manual-%d.log", 1000000000+i))
		if err := os.WriteFile(name, []byte("DEPLOY FAILED: stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(i+1) * time.Hour)
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}

	a := &ServerAgent{transport: NewTransport(Config{Timeout: 5 * time.Second})}
	// FromDisk=true fails fast without a git fetch: the real compose build
	// errors on the missing compose file. The outcome write races our count
	// but only ever rewrites res.LogFile — never a new deploy-manual entry.
	res := a.StartManualDeploy(context.Background(), deploy.ManualDeployRequest{
		FromDisk: true,
		Config:   deploy.RepoConfig{ComposePath: dir},
	})
	if !res.Success {
		t.Fatalf("StartManualDeploy: %s", res.Error)
	}
	t.Cleanup(func() {
		manualDeployMu.Lock()
		delete(manualDeployStates, res.DeployID)
		manualDeployMu.Unlock()
	})

	matches, err := filepath.Glob(filepath.Join(dir, "deploy-manual-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > deploy.MaxDeployTempLogs {
		t.Errorf("deploy-manual-*.log not bounded: %d files, want <= %d (oldest must be pruned at create time)",
			len(matches), deploy.MaxDeployTempLogs)
	}
	// The just-created log for this deploy must survive the prune — it is the
	// one the returned deployID references.
	if _, err := os.Stat(res.LogFile); err != nil {
		t.Errorf("the new deploy log %s must survive the prune: %v", res.LogFile, err)
	}
}

// The legacy deploy-<ts>.log outcome file written by StartDeploy's nohup lane
// is bounded the same way as the manual family — deploy-[0-9]*.log cannot
// match deploy-manual-*.
//
// RED-on-revert: remove the deploy.PruneTempLogs call in StartDeploy — the
// seeded files are never pruned and the count assertion fails.
func TestStartDeploy_PrunesOldDeployLogs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	// Seed more deploy-<ts>.log files than the bound allows, with mtimes
	// far in the past so the prune sees them as the oldest.
	for i := 0; i < deploy.MaxDeployTempLogs+2; i++ {
		name := filepath.Join(dir, fmt.Sprintf("deploy-%d.log", 1700000000+i))
		if err := os.WriteFile(name, []byte("stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(i+1) * time.Hour)
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}

	// The nonexistent project path fails the spawned script's `cd` instantly,
	// so the nohup never reaches docker; the file the test counts is the
	// local log StartDeploy creates before pruning.
	a := &ServerAgent{transport: NewTransport(Config{Host: "local", Timeout: 5 * time.Second})}
	res := a.StartDeploy(context.Background(), "/dozor-test-nonexistent-legacy", nil, false, false)
	if !res.Success {
		t.Fatalf("StartDeploy: %s", res.Error)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "deploy-[0-9]*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > deploy.MaxDeployTempLogs {
		t.Errorf("deploy-*.log not bounded: %d files, want <= %d (oldest must be pruned at create time)",
			len(matches), deploy.MaxDeployTempLogs)
	}
	// The just-created log for this deploy must survive the prune — tied
	// mtimes must never evict the file the deployID references.
	localLog := filepath.Join(dir, res.DeployID+".log")
	if _, err := os.Stat(localLog); err != nil {
		t.Errorf("the new deploy log %s must survive the prune: %v", localLog, err)
	}
}
