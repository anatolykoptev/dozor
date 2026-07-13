package engine

import (
	"context"
	"strings"
	"testing"
)

// cargoStubTransport simulates a host with /mnt/cargo mounted, populated with a
// mix of denylisted, non-cargo-shaped, and valid target dirs — for cleanup_cargo.go
// unit tests. Any command not explicitly matched succeeds with empty output,
// matching the mockSuccessTransport convention used elsewhere in this package.
type cargoStubTransport struct {
	mountOK       bool
	children      []string        // basenames returned by the immediate-children enumeration
	shaped        map[string]bool // basename -> passes the debug/release/.rustc_info.json structural check
	ionicePresent bool
}

func (s *cargoStubTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	switch {
	case strings.HasPrefix(cmd, "mountpoint -q"):
		return CommandResult{Success: s.mountOK}
	case strings.HasPrefix(cmd, "which "):
		tool := strings.TrimSuffix(strings.TrimPrefix(cmd, "which "), " 2>/dev/null")
		if tool == "ionice" {
			if !s.ionicePresent {
				return CommandResult{Success: false}
			}
		}
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	case strings.HasPrefix(cmd, "find /mnt/cargo -mindepth 1 -maxdepth 1"):
		return CommandResult{Success: true, Stdout: strings.Join(s.children, "\n")}
	case strings.HasPrefix(cmd, "test -d"):
		for name, ok := range s.shaped {
			if ok && strings.Contains(cmd, "/mnt/cargo/"+name+"/") {
				return CommandResult{Success: true}
			}
		}
		return CommandResult{Success: false}
	case strings.Contains(cmd, "df -BM"):
		return CommandResult{Success: true, Stdout: "Avail\n10000M\n"}
	case strings.Contains(cmd, "du -sm"):
		return CommandResult{Success: true, Stdout: "100\t/path"}
	default:
		return CommandResult{Success: true}
	}
}

func (s *cargoStubTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true}
}

func (s *cargoStubTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true}
}

func (s *cargoStubTransport) ResolveComposePath() string { return "" }

// --- scanCargo / cleanCargo: mountpoint gate ---

func TestScanCargo_MountpointAbsent_ReturnsUnavailableNoError(t *testing.T) {
	t.Parallel()

	c := &CleanupCollector{transport: &cargoStubTransport{mountOK: false}}
	got := c.scanCargo(context.Background())
	if got.Available {
		t.Error("expected Available=false when /mnt/cargo is not a mountpoint")
	}
	if got.Error != "" {
		t.Errorf("expected no Error for absent mountpoint, got %q", got.Error)
	}
}

func TestCleanCargo_MountpointAbsent_ReturnsUnavailableNoError(t *testing.T) {
	t.Parallel()

	c := &CleanupCollector{transport: &cargoStubTransport{mountOK: false}}
	got := c.cleanCargo(context.Background(), "4d")
	if got.Available {
		t.Error("expected Available=false when /mnt/cargo is not a mountpoint")
	}
	if got.Error != "" {
		t.Errorf("expected no Error for absent mountpoint, got %q", got.Error)
	}
}

// --- denylist ---

func TestCleanCargo_DenylistNeverTouched(t *testing.T) {
	t.Parallel()

	denylisted := []string{"sccache-shared", "cargo-registry-shared", "cargo-git-shared", "backups", "lost+found"}
	mock := &cargoStubTransport{
		mountOK:  true,
		children: denylisted,
		shaped:   map[string]bool{}, // even if (hypothetically) shaped, denylist must win
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	for _, cmd := range rec.cmds {
		for _, denied := range denylisted {
			if strings.Contains(cmd, denied) {
				t.Errorf("denylisted dir %q referenced in command: %q", denied, cmd)
			}
		}
	}
}

func TestScanCargo_DenylistExcludedFromSizeProbe(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:  true,
		children: []string{"sccache-shared", "myrepo-shared-abc123"},
		shaped:   map[string]bool{"myrepo-shared-abc123": true},
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	got := c.scanCargo(context.Background())
	if !got.Available {
		t.Fatal("expected Available=true")
	}
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "du -sm") && strings.Contains(cmd, "sccache-shared") {
			t.Errorf("scanCargo must never du the denylisted sccache-shared dir, got: %q", cmd)
		}
	}
}

// --- structural validation (fail-closed) ---

func TestCleanCargo_NonCargoShapedDirSkipped(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:  true,
		children: []string{"oxpulse-d4-apns-isolated"},
		shaped:   map[string]bool{"oxpulse-d4-apns-isolated": false},
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "-delete") && strings.Contains(cmd, "oxpulse-d4-apns-isolated") {
			t.Errorf("non-cargo-shaped dir must never be pruned, got command: %q", cmd)
		}
	}
}

// --- .cargo-lock exclusion ---

func TestCleanCargo_ExcludesCargoLockFromDeletion(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:  true,
		children: []string{"myrepo-shared-abc123"},
		shaped:   map[string]bool{"myrepo-shared-abc123": true},
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	found := false
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "myrepo-shared-abc123") && strings.Contains(cmd, "-delete") && strings.Contains(cmd, "-atime") {
			found = true
			if !strings.Contains(cmd, `! -name '.cargo-lock'`) {
				t.Errorf("expected .cargo-lock exclusion in prune command, got: %q", cmd)
			}
		}
	}
	if !found {
		t.Fatal("expected a prune command for the valid target dir, found none")
	}
}

// --- age threshold plumbing ---

func TestCleanCargo_AgeThresholdPlumbedThrough(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:  true,
		children: []string{"myrepo-shared-abc123"},
		shaped:   map[string]bool{"myrepo-shared-abc123": true},
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "10d")

	found := false
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "-atime +10") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected -atime +10 (from minAge=10d) in prune command, got commands: %v", rec.cmds)
	}
}

// --- empty-subdir cleanup ---

func TestCleanCargo_RemovesEmptySubdirsAfterPrune(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:  true,
		children: []string{"myrepo-shared-abc123"},
		shaped:   map[string]bool{"myrepo-shared-abc123": true},
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	found := false
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "myrepo-shared-abc123") && strings.Contains(cmd, "-empty -delete") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected empty-subdir cleanup command for valid target dir, got: %v", rec.cmds)
	}
}

// --- regression guard: the incident-causing pattern must never return ---

func TestCleanCargo_NeverIssuesBareRmCommand(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:  true,
		children: []string{"myrepo-shared-abc123"},
		shaped:   map[string]bool{"myrepo-shared-abc123": true},
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "rm -rf") || strings.Contains(cmd, "rm -r ") {
			t.Errorf("cleanCargo must prune via find -delete, never rm -rf; got: %q", cmd)
		}
	}
}

// --- ionice best-effort throttling ---

func TestCleanCargo_IonicePresent_PrefixesFindCommand(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:       true,
		children:      []string{"myrepo-shared-abc123"},
		shaped:        map[string]bool{"myrepo-shared-abc123": true},
		ionicePresent: true,
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	found := false
	for _, cmd := range rec.cmds {
		if strings.HasPrefix(cmd, "nice -n 19 ionice -c3 find") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected nice+ionice prefix on the prune find command, got: %v", rec.cmds)
	}
}

func TestCleanCargo_IoniceAbsent_FallsBackToNiceOnly(t *testing.T) {
	t.Parallel()

	mock := &cargoStubTransport{
		mountOK:       true,
		children:      []string{"myrepo-shared-abc123"},
		shaped:        map[string]bool{"myrepo-shared-abc123": true},
		ionicePresent: false,
	}
	rec := &recordingTransport{inner: mock}
	c := &CleanupCollector{transport: rec}
	c.cleanCargo(context.Background(), "4d")

	found := false
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "myrepo-shared-abc123") && strings.Contains(cmd, "-atime") {
			if strings.Contains(cmd, "ionice") {
				t.Errorf("expected no ionice in command when binary absent, got: %q", cmd)
			}
			if !strings.HasPrefix(cmd, "nice -n 19 find") {
				t.Errorf("expected nice-only prefix (no hard-fail on missing ionice), got: %q", cmd)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected a prune command, found none")
	}
}
