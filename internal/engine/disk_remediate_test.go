package engine

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// newTestAgent creates a minimal ServerAgent with a local transport for unit tests.
// Commands that fail (e.g. journalctl not available) produce CleanupTarget{Available:false},
// which is a valid "nothing to do" result — tests check routing, not actual bytes freed.
func newTestAgent() *ServerAgent {
	cfg := Config{}
	t := NewTransport(cfg)
	return &ServerAgent{
		cfg:       cfg,
		transport: t,
		cleanup:   &CleanupCollector{transport: t},
	}
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
