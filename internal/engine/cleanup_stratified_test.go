package engine

import (
	"context"
	"strings"
	"testing"
)

// --- cleanAptCache tests ---

// TestCleanAptCache_NoSudoSkipped verifies that when sudo -n apt-get clean returns
// "permission denied", cleanAptCache returns FreedMB=0 with an error noting the skip.
func TestCleanAptCache_NoSudoSkipped(t *testing.T) {
	t.Parallel()

	mock := &mockTransport{failWith: "permission denied"}
	c := &CleanupCollector{transport: mock}
	got := c.cleanAptCache(context.Background())
	if got.FreedMB != 0 {
		t.Errorf("expected FreedMB=0 when sudo fails, got %v", got.FreedMB)
	}
	if got.Error == "" {
		t.Error("expected non-empty Error when sudo fails, got empty")
	}
}

// TestCleanAptCache_Success verifies that when sudo -n apt-get clean succeeds,
// cleanAptCache sets Available=true and FreedMB >= 0.
func TestCleanAptCache_Success(t *testing.T) {
	t.Parallel()

	// succeedFor returns success for all commands (sudo + df).
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
