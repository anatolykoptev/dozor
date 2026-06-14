package engine

import (
	"context"
	"strings"
	"testing"
)

// --- cleanAptCache tests ---

// TestCleanAptCache_SudoStructurallyUnavailable verifies that when sudo cannot run due to
// NoNewPrivileges (or /etc/sudo.conf ownership), cleanAptCache returns Available=false
// with no Error — so it does NOT contribute to the partial-WARN aggregate.
// This is the expected state when dozor runs under systemd with NoNewPrivileges=yes.
func TestCleanAptCache_SudoStructurallyUnavailable(t *testing.T) {
	t.Parallel()

	// noNewPrivTransport simulates the exact error emitted by sudo under NoNewPrivileges.
	mock := &sudoUnavailableTransport{
		sudoErr: `sudo: /etc/sudo.conf is owned by uid 65534, should be 0
sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`,
	}
	c := &CleanupCollector{transport: mock}
	got := c.cleanAptCache(context.Background())

	// Available must be false: the target is structurally skipped, not failed.
	if got.Available {
		t.Error("expected Available=false when sudo is structurally unavailable")
	}
	// Error must be empty: structural skip must NOT pollute res.Errors.
	if got.Error != "" {
		t.Errorf("expected empty Error for structural sudo unavailability, got %q", got.Error)
	}
	if got.FreedMB != 0 {
		t.Errorf("expected FreedMB=0 when sudo unavailable, got %v", got.FreedMB)
	}
}

// TestCleanAptCache_SudoUnavailableNoErrorInAggregate verifies that when sudo is
// unavailable (any reason), the apt target produces no error in the DiskRemediateResult
// aggregate — so it cannot trigger "disk auto-remediate partial" WARN on its own.
// Red-on-revert: if cleanAptCache sets t.Error on sudo failure, appendTarget pushes to
// res.Errors, and this test will fail because res.Errors will be non-empty from apt alone.
func TestCleanAptCache_SudoUnavailableNoErrorInAggregate(t *testing.T) {
	t.Parallel()

	// Use a transport where ONLY sudo fails (no-new-privileges style), all other tools succeed.
	mock := &sudoUnavailableTransport{
		sudoErr: `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`,
	}
	a := &ServerAgent{
		cfg:       Config{},
		transport: NewTransport(Config{}),
		cleanup:   &CleanupCollector{transport: mock},
	}
	res := a.AutoRemediateDisk(context.Background(), AlertWarning)
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	// apt must not appear in errors.
	for _, e := range res.Errors {
		if strings.Contains(e, "apt") {
			t.Errorf("apt must not contribute to res.Errors when sudo is structurally unavailable; got: %v", res.Errors)
		}
	}
}

// TestCleanAptCache_SudoAvailableAptFails verifies that when sudo IS available but
// apt-get clean fails, the error IS surfaced (real failure, not structural skip).
func TestCleanAptCache_SudoAvailableAptFails(t *testing.T) {
	t.Parallel()

	// sudoOKAptFailTransport: sudo probe succeeds, apt-get clean fails.
	mock := &sudoOKAptFailTransport{aptErr: "E: some apt error"}
	c := &CleanupCollector{transport: mock}
	got := c.cleanAptCache(context.Background())

	if got.Available {
		t.Error("expected Available=false when apt-get clean fails")
	}
	if got.Error == "" {
		t.Error("expected non-empty Error when apt-get clean fails despite sudo being available")
	}
	if !strings.Contains(got.Error, "apt error") {
		t.Errorf("expected error to contain 'apt error', got %q", got.Error)
	}
}

// TestCleanAptCache_Success verifies that when sudo -n apt-get clean succeeds,
// cleanAptCache sets Available=true and FreedMB >= 0.
func TestCleanAptCache_Success(t *testing.T) {
	t.Parallel()

	// mockSuccessTransport returns success for all commands (sudo probe + apt-get clean + df).
	mock := &mockSuccessTransport{}
	c := &CleanupCollector{transport: mock}
	got := c.cleanAptCache(context.Background())
	if !got.Available {
		t.Error("expected Available=true on success")
	}
	if got.Error != "" {
		t.Errorf("expected no error on success, got %q", got.Error)
	}
	// FreedMB is 0 because mockSuccessTransport df always returns same value.
	// Just assert no panic and no error.
}

// --- apt-specific transport stubs ---

// sudoUnavailableTransport simulates a host where sudo is structurally unavailable
// (NoNewPrivileges / ownership error) but all non-sudo commands succeed.
type sudoUnavailableTransport struct {
	sudoErr string // error text returned by "sudo -n ..."
}

func (s *sudoUnavailableTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	if strings.HasPrefix(cmd, "which ") {
		tool := strings.TrimPrefix(cmd, "which ")
		tool = strings.TrimSuffix(tool, " 2>/dev/null")
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	}
	if strings.Contains(cmd, "df -BM") {
		return CommandResult{Success: true, Stdout: "Avail\n10000M\n"}
	}
	if strings.Contains(cmd, "du -sm") {
		return CommandResult{Success: true, Stdout: "100\t/path"}
	}
	if strings.HasPrefix(cmd, "sudo ") {
		// Return the structural error as stdout (sudo sends to stdout when 2>&1 merged).
		return CommandResult{Success: false, Stdout: s.sudoErr}
	}
	return CommandResult{Success: true, Stdout: ""}
}

func (s *sudoUnavailableTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: "Total reclaimed space: 0B"}
}

func (s *sudoUnavailableTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (s *sudoUnavailableTransport) ResolveComposePath() string { return "" }

// sudoOKAptFailTransport simulates a host where sudo is available but apt-get clean fails.
type sudoOKAptFailTransport struct {
	aptErr string // error text returned by apt-get clean
}

func (s *sudoOKAptFailTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	if strings.HasPrefix(cmd, "which ") {
		tool := strings.TrimPrefix(cmd, "which ")
		tool = strings.TrimSuffix(tool, " 2>/dev/null")
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	}
	if strings.Contains(cmd, "df -BM") {
		return CommandResult{Success: true, Stdout: "Avail\n10000M\n"}
	}
	// sudo -n true (probe) succeeds; sudo -n apt-get clean fails.
	if strings.Contains(cmd, "sudo -n true") {
		return CommandResult{Success: true, Stdout: ""}
	}
	if strings.Contains(cmd, "apt-get") {
		return CommandResult{Success: false, Stdout: s.aptErr}
	}
	return CommandResult{Success: true, Stdout: ""}
}

func (s *sudoOKAptFailTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: "Total reclaimed space: 0B"}
}

func (s *sudoOKAptFailTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (s *sudoOKAptFailTransport) ResolveComposePath() string { return "" }

// --- cleanSccache tests ---

// TestCleanSccache_MissingDirSkipped verifies that when ~/.cache/sccache does not exist,
// cleanSccache returns an empty (skipped) target — not an error.
func TestCleanSccache_MissingDirSkipped(t *testing.T) {
	t.Parallel()

	// dir-missing transport: du returns failure (non-zero exit)
	mock := &mockTransport{failWith: "No such file or directory"}
	c := &CleanupCollector{transport: mock}
	got := c.cleanSccache(context.Background())
	// When dir missing (du fails), Available should be false and no error set.
	if got.Available {
		t.Error("expected Available=false for missing sccache dir")
	}
	if got.Error != "" {
		t.Errorf("expected no Error for missing dir, got %q", got.Error)
	}
}

// TestCleanSccache_NukesContents verifies that when ~/.cache/sccache exists,
// cleanSccache issues the rm -rf command targeting the sccache path.
func TestCleanSccache_NukesContents(t *testing.T) {
	t.Parallel()

	rec := &recordingTransport{
		// du succeeds first call (exists check), rest succeed too
		inner: &mockSuccessTransport{},
	}
	c := &CleanupCollector{transport: rec}
	got := c.cleanSccache(context.Background())
	// Must issue rm against sccache path.
	found := false
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, "sccache") && strings.Contains(cmd, "rm") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected rm command targeting sccache path, got commands: %v", rec.cmds)
	}
	_ = got // result validated via commands
}

// --- cleanNpmYarn tests ---

// TestCleanNpmYarn_RemovesBothCaches verifies cleanNpmYarn targets both
// ~/.npm/_cacache and ~/.cache/yarn.
func TestCleanNpmYarn_RemovesBothCaches(t *testing.T) {
	t.Parallel()

	rec := &recordingTransport{inner: &mockSuccessTransport{}}
	c := &CleanupCollector{transport: rec}
	_ = c.cleanNpmYarn(context.Background())

	cmds := strings.Join(rec.cmds, " ")
	if !strings.Contains(cmds, ".npm/_cacache") {
		t.Errorf("expected .npm/_cacache in commands, got: %v", rec.cmds)
	}
	if !strings.Contains(cmds, ".cache/yarn") {
		t.Errorf("expected .cache/yarn in commands, got: %v", rec.cmds)
	}
}

// --- cleanDockerDangling tests ---

// TestCleanDockerDangling_ParsesFreedBytes verifies that docker output
// "Total reclaimed space: 1.234GB" is parsed to ~1263.6 MB.
func TestCleanDockerDangling_ParsesFreedBytes(t *testing.T) {
	t.Parallel()

	mock := &dockerOutputTransport{output: "Total reclaimed space: 1.234GB"}
	c := &CleanupCollector{transport: mock}
	got := c.cleanDockerDangling(context.Background())
	// ParseSizeMB("1.234GB") = 1263.6...
	if got.FreedMB < 1263 || got.FreedMB > 1264 {
		t.Errorf("expected FreedMB ~1263.6, got %v", got.FreedMB)
	}
}

// TestCleanDockerDangling_UsesFilterDangling verifies that the docker command
// includes "dangling=true" filter.
func TestCleanDockerDangling_UsesFilterDangling(t *testing.T) {
	t.Parallel()

	rec := &recordingTransport{inner: &mockSuccessTransport{}}
	c := &CleanupCollector{transport: rec}
	_ = c.cleanDockerDangling(context.Background())

	found := false
	for _, cmd := range rec.dockerCmds {
		if strings.Contains(cmd, "dangling=true") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dangling=true in docker commands, got: %v", rec.dockerCmds)
	}
}

// --- cleanDockerBuilderAged tests ---

// TestCleanDockerBuilderAged_PassesAgeFilter verifies that the docker builder prune
// command includes "--filter until=72h".
func TestCleanDockerBuilderAged_PassesAgeFilter(t *testing.T) {
	t.Parallel()

	rec := &recordingTransport{inner: &mockSuccessTransport{}}
	c := &CleanupCollector{transport: rec}
	_ = c.cleanDockerBuilderAged(context.Background(), "72h")

	found := false
	for _, cmd := range rec.dockerCmds {
		if strings.Contains(cmd, "until=72h") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected until=72h in docker commands, got: %v", rec.dockerCmds)
	}
}

// --- AutoRemediateDisk level dispatching tests ---

// TestAutoRemediateDisk_LevelDispatching is a table-driven test that verifies
// each alert level triggers the expected set of cleanup targets.
func TestAutoRemediateDisk_LevelDispatching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level         AlertLevel
		expectNil     bool
		mustContain   []string // target names that must be present
		mustNotContain []string // target names that must NOT be present
	}{
		{
			level:     AlertInfo,
			expectNil: true,
		},
		{
			level:       AlertWarning,
			mustContain: []string{"journal", "tmp", "caches", "apt", "sccache", "npm_yarn", "docker_dangling"},
			mustNotContain: []string{"docker_builder_aged"},
		},
		{
			level:       AlertWarningHigh,
			mustContain: []string{"journal", "tmp", "caches", "apt", "sccache", "npm_yarn", "docker_dangling", "docker_builder_aged"},
		},
		{
			level:       AlertCritical,
			mustContain: []string{"journal", "tmp", "caches"},
		},
		{
			level:       AlertError,
			mustContain: []string{"journal", "tmp", "caches"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.level), func(t *testing.T) {
			t.Parallel()
			a := newTestAgent()
			res := a.AutoRemediateDisk(context.Background(), tt.level)
			if tt.expectNil {
				if res != nil {
					t.Errorf("level %s: expected nil result, got %+v", tt.level, res)
				}
				return
			}
			if res == nil {
				t.Fatalf("level %s: expected non-nil result", tt.level)
			}
			names := make(map[string]bool, len(res.Targets))
			for _, tgt := range res.Targets {
				names[tgt.Name] = true
			}
			for _, want := range tt.mustContain {
				if !names[want] {
					t.Errorf("level %s: missing target %q; got: %v", tt.level, want, res.Targets)
				}
			}
			for _, notWant := range tt.mustNotContain {
				if names[notWant] {
					t.Errorf("level %s: should not contain target %q; got: %v", tt.level, notWant, res.Targets)
				}
			}
		})
	}
}

// --- helper transports for tests ---

// mockSuccessTransport returns success for all commands with empty output.
// "which X" returns "/usr/bin/X" so probe() returns true.
// df commands return a stable value so measureFreedMB returns 0 (no change).
type mockSuccessTransport struct{}

func (m *mockSuccessTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	if strings.HasPrefix(cmd, "which ") {
		tool := strings.TrimPrefix(cmd, "which ")
		tool = strings.TrimSuffix(tool, " 2>/dev/null")
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	}
	if strings.Contains(cmd, "df -BM") {
		return CommandResult{Success: true, Stdout: "Avail\n10000M\n"}
	}
	if strings.Contains(cmd, "du -sm") {
		// Simulate dir exists by returning a size.
		return CommandResult{Success: true, Stdout: "100\t/path"}
	}
	return CommandResult{Success: true, Stdout: ""}
}

func (m *mockSuccessTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: "Total reclaimed space: 0B"}
}

func (m *mockSuccessTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (m *mockSuccessTransport) ResolveComposePath() string { return "" }

// recordingTransport wraps another transport and records all executed commands.
type recordingTransport struct {
	inner      Transporter
	cmds       []string
	dockerCmds []string
}

func (r *recordingTransport) ExecuteUnsafe(ctx context.Context, cmd string) CommandResult {
	r.cmds = append(r.cmds, cmd)
	return r.inner.ExecuteUnsafe(ctx, cmd)
}

func (r *recordingTransport) DockerCommand(ctx context.Context, dockerCmd string) CommandResult {
	r.dockerCmds = append(r.dockerCmds, dockerCmd)
	return r.inner.DockerCommand(ctx, dockerCmd)
}

func (r *recordingTransport) DockerComposeCommand(ctx context.Context, composeCmd string) CommandResult {
	return r.inner.DockerComposeCommand(ctx, composeCmd)
}

func (r *recordingTransport) ResolveComposePath() string { return r.inner.ResolveComposePath() }

// dockerOutputTransport returns canned output for DockerCommand and success for everything else.
type dockerOutputTransport struct {
	output string
}

func (d *dockerOutputTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	if strings.Contains(cmd, "df -BM") {
		return CommandResult{Success: true, Stdout: "Avail\n10000M\n"}
	}
	return CommandResult{Success: true, Stdout: ""}
}

func (d *dockerOutputTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: d.output}
}

func (d *dockerOutputTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (d *dockerOutputTransport) ResolveComposePath() string { return "" }
