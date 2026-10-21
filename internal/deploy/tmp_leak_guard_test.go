package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/testutil/tempguard"
)

// The dozor-build-*.log dump is intentionally kept — its path is surfaced in
// the build error for the operator — but the family must stay bounded:
// runBuildWithFullLog prunes it to the newest MaxDeployTempLogs at write
// time. These dumps were the bulk of the /tmp accumulation.
//
// RED-on-revert: remove the PruneTempLogs call in runBuildWithFullLog — the
// seeded files are never pruned and the count assertion fails.
func TestRunBuildWithFullLog_PrunesOldDumps(t *testing.T) {
	tempguard.Isolate(t)
	seedOldLogs(t, "dozor-build-seed-", MaxDeployTempLogs+2)

	origBuild := buildRunner
	t.Cleanup(func() { buildRunner = origBuild })
	buildRunner = func(context.Context, string, []string) ([]byte, error) {
		return []byte("build boom"), errors.New("exit status 1")
	}

	errMsg := runBuildWithFullLog(context.Background(), BuildRequest{CommitSHA: "abc1234"}, nil)
	if errMsg == "" {
		t.Fatal("expected a build failure")
	}

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "dozor-build-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > MaxDeployTempLogs {
		t.Errorf("dozor-build-*.log not bounded: %d files, want <= %d (oldest must be pruned at write time)",
			len(matches), MaxDeployTempLogs)
	}
}

// Same bound for the up-phase dump family.
//
// RED-on-revert: remove the PruneTempLogs call in runUpWithFullLog.
func TestRunUpWithFullLog_PrunesOldDumps(t *testing.T) {
	tempguard.Isolate(t)
	seedOldLogs(t, "dozor-up-seed-", MaxDeployTempLogs+2)

	origUp := upRunner
	t.Cleanup(func() { upRunner = origUp })
	upRunner = func(context.Context, string, []string) ([]byte, error) {
		return []byte("up boom"), errors.New("exit status 1")
	}

	errMsg := runUpWithFullLog(context.Background(), BuildRequest{
		CommitSHA: "abc1234",
		Config:    RepoConfig{Services: []string{"svc"}},
	}, "deploy-abc")
	if errMsg == "" {
		t.Fatal("expected an up failure")
	}

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "dozor-up-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > MaxDeployTempLogs {
		t.Errorf("dozor-up-*.log not bounded: %d files, want <= %d (oldest must be pruned at write time)",
			len(matches), MaxDeployTempLogs)
	}
}

// seedOldLogs writes n matching files into the current temp dir with mtimes
// far in the past, so the prune sees them as the oldest of their family.
func seedOldLogs(t *testing.T, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := filepath.Join(os.TempDir(), fmt.Sprintf("%s%03d.log", prefix, i))
		if err := os.WriteFile(name, []byte("stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(i+1) * time.Hour)
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
}
