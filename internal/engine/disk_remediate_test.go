package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestAgent creates a minimal ServerAgent with a local transport for unit tests.
// Commands that fail (e.g. journalctl not available) produce CleanupTarget{Available:false},
// which is a valid "nothing to do" result — tests check routing, not actual bytes freed.
//
// cleanup.transport is wrapped in cargoMountGuardTransport: krolik (the box these
// tests run on) has a REAL, production /mnt/cargo mount (77G+ of live shared
// worktree build-cache as of 2026-07-12, including a 50G+ live sccache-shared
// dir). Without the guard, any test that drives AutoRemediateDisk through the
// WARNING_HIGH+ (cargo) or CRITICAL+ (sccache) tier with a real transport
// would have cleanCargo/cleanSccache actually enumerate/prune or rm -rf that
// live data as a side effect of running `go test` — exactly the class of
// accidental-touch this task's cargoDenylist/structural-validation is meant
// to prevent, just via a different door. The guard forces the /mnt/cargo
// mountpoint probe, any command against cargoRoot+"/sccache-shared", and any
// command against the real resolved home-dir sccache path to report
// "absent" — cleanCargo/cleanSccache cleanly report Available=false; every
// other command still hits the real host exactly as before.
func newTestAgent() *ServerAgent {
	cfg := Config{}
	t := NewTransport(cfg)
	return &ServerAgent{
		cfg:       cfg,
		transport: t,
		cleanup:   &CleanupCollector{transport: cargoMountGuardTransport{t}},
	}
}

// cargoMountGuardTransport wraps a real Transporter but forces the /mnt/cargo
// mountpoint probe, the cargoRoot+"/sccache-shared" path, and the real
// home-dir sccache fallback path to report failure — see newTestAgent for
// why this exists.
type cargoMountGuardTransport struct {
	Transporter
}

// guardedSccachePaths are the sccache candidate paths cleanSccache would
// otherwise probe/rm for real against this host — see newTestAgent's doc
// comment. Computed once; homeSccache is skipped (never guarded, harmless
// to hit for real) if the home dir can't be resolved.
var guardedSccachePaths = func() []string {
	paths := []string{cargoRoot + "/sccache-shared"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".cache", "sccache"))
	}
	return paths
}()

func (g cargoMountGuardTransport) ExecuteUnsafe(ctx context.Context, cmd string) CommandResult {
	if strings.HasPrefix(cmd, "mountpoint -q "+cargoRoot) {
		return CommandResult{Success: false}
	}
	for _, p := range guardedSccachePaths {
		if strings.Contains(cmd, p) {
			return CommandResult{Success: false, Stderr: "No such file or directory"}
		}
	}
	return g.Transporter.ExecuteUnsafe(ctx, cmd)
}

func TestAutoDiskRemediate_InfoLevel_ReturnsNil(t *testing.T) {
	t.Parallel()

	a := newTestAgent()
	res := a.AutoRemediateDisk(context.Background(), AlertInfo)
	if res != nil {
		t.Errorf("AlertInfo: expected nil result, got %+v", res)
	}
}

func TestAutoDiskRemediate_WarningLevel_ReturnsResult(t *testing.T) {
	t.Parallel()

	a := newTestAgent()
	res := a.AutoRemediateDisk(context.Background(), AlertWarning)
	if res == nil {
		t.Fatal("AlertWarning: expected non-nil result")
	}
	// Must include journal, tmp, caches targets.
	names := make(map[string]bool, len(res.Targets))
	for _, tgt := range res.Targets {
		names[tgt.Name] = true
	}
	for _, want := range []string{"journal", "tmp", "caches"} {
		if !names[want] {
			t.Errorf("AlertWarning: missing target %q in result %v", want, res.Targets)
		}
	}
	// sccache moved to CRITICAL-only (see disk_remediate.go doc comment): it is
	// LRU-managed at a fixed cap by design, so nuking it at plain Warning would
	// routinely wipe the fleet's build-cache accelerator for a non-emergency level.
	// cargo only starts at WARNING_HIGH+.
	for _, notWant := range []string{"sccache", "cargo"} {
		if names[notWant] {
			t.Errorf("AlertWarning: target %q must not run at plain Warning, got %v", notWant, res.Targets)
		}
	}
	// Docker prune must NOT run on Warning.
	if res.Docker != "" {
		t.Errorf("AlertWarning: Docker prune should not run on Warning, got %q", res.Docker)
	}
}

func TestAutoDiskRemediate_CriticalLevel_IncludesDockerPrune(t *testing.T) {
	t.Parallel()

	a := newTestAgent()
	res := a.AutoRemediateDisk(context.Background(), AlertCritical)
	if res == nil {
		t.Fatal("AlertCritical: expected non-nil result")
	}
	// Must include journal, tmp, caches targets.
	names := make(map[string]bool, len(res.Targets))
	for _, tgt := range res.Targets {
		names[tgt.Name] = true
	}
	for _, want := range []string{"journal", "tmp", "caches"} {
		if !names[want] {
			t.Errorf("AlertCritical: missing target %q in result %v", want, res.Targets)
		}
	}
	// Docker prune MUST run on Critical — verify the field is non-empty when docker is available.
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available in this environment; skipping Docker field assertion")
	}
	if res.Docker == "" {
		t.Errorf("AlertCritical: expected non-empty Docker field when docker is available, got empty string")
	}
}

func TestAutoDiskRemediate_ErrorLevel_TreatedAsCritical(t *testing.T) {
	t.Parallel()

	a := newTestAgent()
	// AlertError should also trigger cleanup (treated as AlertCritical path).
	res := a.AutoRemediateDisk(context.Background(), AlertError)
	if res == nil {
		t.Fatal("AlertError: expected non-nil result (same path as Critical)")
	}
}

func TestAutoDiskRemediate_NilCleanup_ReturnsNil(t *testing.T) {
	t.Parallel()

	a := &ServerAgent{} // cleanup is nil
	res := a.AutoRemediateDisk(context.Background(), AlertWarning)
	if res != nil {
		t.Errorf("nil cleanup: expected nil result, got %+v", res)
	}
}

func TestAutoDiskRemediate_TargetErrorsAggregated(t *testing.T) {
	t.Parallel()

	// Use a mock transport that always returns failure so cleanJournal / cleanTmp / cleanCaches
	// each produce a CleanupTarget.Error — which AutoRemediateDisk must aggregate into res.Errors.
	mock := &mockTransport{failWith: "permission denied"}
	a := &ServerAgent{
		cfg:       Config{},
		transport: NewTransport(Config{}),
		cleanup:   &CleanupCollector{transport: mock},
	}
	res := a.AutoRemediateDisk(context.Background(), AlertWarning)
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	// At least one target must have propagated the injected error.
	if len(res.Errors) == 0 {
		t.Errorf("expected at least one error in res.Errors, got empty slice; targets: %+v", res.Targets)
	}
	// Confirm the injected error string is present.
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "permission denied") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'permission denied' in res.Errors, got: %v", res.Errors)
	}
}

// mockTransport is a Transporter that simulates a host where tools are present but fail with
// a canned stderr message. "which <tool>" succeeds so probe() passes; all other commands fail.
// This lets cleanJournal/cleanTmp/cleanCaches reach their execution branch and produce CleanupTarget.Error.
type mockTransport struct {
	failWith string
}

func (m *mockTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	// Simulate tool-present: "which <tool>" returns tool name so probe() returns true.
	if strings.HasPrefix(cmd, "which ") {
		tool := strings.TrimPrefix(cmd, "which ")
		tool = strings.TrimSuffix(tool, " 2>/dev/null")
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	}
	// All other commands fail with the canned error.
	return CommandResult{Success: false, Stderr: m.failWith}
}

func (m *mockTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: false, Stderr: m.failWith}
}

func (m *mockTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: false, Stderr: m.failWith}
}

func (m *mockTransport) ResolveComposePath() string { return "" }

// TestExtractIssues_DiskCriticalLine asserts that a [CRITICAL] disk line emitted by
// appendDiskPressure is parsed into a TriageIssue with Service=="disk".
func TestExtractIssues_DiskCriticalLine(t *testing.T) {
	t.Parallel()

	report := "System Overview\n\nDisk: /dev/sda1 91% (4.2G free) — 🔴\n[CRITICAL] disk — /dev/sda1 at 91% (4.2GB free)\n"
	issues := ExtractIssues(report)
	if len(issues) == 0 {
		t.Fatal("ExtractIssues: expected at least one issue for [CRITICAL] disk line, got none")
	}
	found := false
	for _, iss := range issues {
		if iss.Service == "disk" {
			found = true
			if !strings.Contains(iss.Description, "91%") {
				t.Errorf("issue description should contain '91%%', got %q", iss.Description)
			}
			break
		}
	}
	if !found {
		t.Errorf("ExtractIssues: expected TriageIssue with Service=disk, got %+v", issues)
	}
}

// TestExtractIssues_DiskWarningLine asserts that a [WARNING] disk line is parsed into
// a TriageIssue with Service=="disk".
func TestExtractIssues_DiskWarningLine(t *testing.T) {
	t.Parallel()

	report := "System Overview\n\nDisk: /mnt/cargo 82% (8.5G free) — ⚠️\n[WARNING] disk — /mnt/cargo at 82% (8.5GB free)\n"
	issues := ExtractIssues(report)
	if len(issues) == 0 {
		t.Fatal("ExtractIssues: expected at least one issue for [WARNING] disk line, got none")
	}
	found := false
	for _, iss := range issues {
		if iss.Service == "disk" {
			found = true
			if !strings.Contains(iss.Description, "82%") {
				t.Errorf("issue description should contain '82%%', got %q", iss.Description)
			}
			break
		}
	}
	if !found {
		t.Errorf("ExtractIssues: expected TriageIssue with Service=disk, got %+v", issues)
	}
}

// TestExtractIssues_DiskWarningHighLine asserts that a [WARNING_HIGH] disk line is parsed
// into a TriageIssue with Service=="disk".
func TestExtractIssues_DiskWarningHighLine(t *testing.T) {
	t.Parallel()

	report := "System Overview\n\nDisk: /dev/sda1 88% (20G free) — ⚠️\n[WARNING_HIGH] disk — /dev/sda1 at 88% (20.0GB free)\n"
	issues := ExtractIssues(report)
	if len(issues) == 0 {
		t.Fatal("ExtractIssues: expected at least one issue for [WARNING_HIGH] disk line, got none")
	}
	found := false
	for _, iss := range issues {
		if iss.Service == "disk" {
			found = true
			if !strings.Contains(iss.Description, "88%") {
				t.Errorf("issue description should contain '88%%%%', got %q", iss.Description)
			}
			break
		}
	}
	if !found {
		t.Errorf("ExtractIssues: expected TriageIssue with Service=disk for WARNING_HIGH, got %+v", issues)
	}
}

// TestExtractIssues_NoDiskLine asserts that a report without machine-readable disk lines
// produces no disk issues — confirming the old human-readable format was invisible to ExtractIssues.
func TestExtractIssues_NoDiskLine(t *testing.T) {
	t.Parallel()

	// Old format without machine-readable line — no disk issues expected.
	report := "System Overview\n\nDisk: /dev/sda1 91% (4.2G free) — 🔴\n"
	issues := ExtractIssues(report)
	for _, iss := range issues {
		if iss.Service == "disk" {
			t.Errorf("ExtractIssues: did not expect disk issue from human-readable-only format, got %+v", iss)
		}
	}
}

// --- DOZOR_CLEANUP_AGE_DAYS env parsing ---

// TestCleanupAgeDays_Unset verifies that an unset (empty) DOZOR_CLEANUP_AGE_DAYS
// falls back to cleanupAgeDaysDefault (4).
func TestCleanupAgeDays_Unset(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_CLEANUP_AGE_DAYS", "")

	if got := cleanupAgeDays(); got != cleanupAgeDaysDefault {
		t.Errorf("expected default %d when unset, got %d", cleanupAgeDaysDefault, got)
	}
}

// TestCleanupAgeDays_ValidOverride verifies that a valid positive integer overrides
// the default.
func TestCleanupAgeDays_ValidOverride(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_CLEANUP_AGE_DAYS", "10")

	if got := cleanupAgeDays(); got != 10 {
		t.Errorf("expected override 10, got %d", got)
	}
}

// TestCleanupAgeDays_InvalidFallsBackToDefault verifies that a non-numeric value
// falls back to cleanupAgeDaysDefault (and would log a WARN, not asserted here).
func TestCleanupAgeDays_InvalidFallsBackToDefault(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_CLEANUP_AGE_DAYS", "not-a-number")

	if got := cleanupAgeDays(); got != cleanupAgeDaysDefault {
		t.Errorf("expected fallback default %d for invalid value, got %d", cleanupAgeDaysDefault, got)
	}
}

// TestCleanupAgeDays_ZeroOrNegativeFallsBackToDefault verifies that a
// non-positive value (which would produce a nonsensical or unbounded find
// -atime filter) falls back to the default rather than being accepted as-is.
func TestCleanupAgeDays_ZeroOrNegativeFallsBackToDefault(t *testing.T) {
	// No t.Parallel() — t.Setenv requires sequential test.
	t.Setenv("DOZOR_CLEANUP_AGE_DAYS", "0")

	if got := cleanupAgeDays(); got != cleanupAgeDaysDefault {
		t.Errorf("expected fallback default %d for zero value, got %d", cleanupAgeDaysDefault, got)
	}
}
