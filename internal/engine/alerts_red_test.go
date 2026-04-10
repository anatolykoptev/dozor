package engine

import (
	"testing"
)

func TestGetAlertLevel_ZeroRecentRestarts_HighErrorCount_ReturnsWarningNotError(t *testing.T) {
	t.Parallel()

	// ErrorCount ≤ maxRecentErrors with 0 restarts → Warning, not Error.
	// maxRecentErrors = 5; ErrorCount=5 is not > maxRecentErrors, so Warning.
	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: 0,
		ErrorCount:     maxRecentErrors,
	}
	level := s.GetAlertLevel()
	if level == AlertError {
		t.Errorf("ErrorCount=%d (==maxRecentErrors, not >): expected Warning or Info, got Error", maxRecentErrors)
	}
	if level != AlertWarning {
		t.Errorf("ErrorCount=%d with no restarts: expected Warning, got %s", maxRecentErrors, level)
	}
}

func TestGetAlertLevel_ErrorCountAboveMax_ReturnsError(t *testing.T) {
	t.Parallel()

	// ErrorCount > maxRecentErrors should yield AlertError.
	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: 0,
		ErrorCount:     maxRecentErrors + 1,
	}
	level := s.GetAlertLevel()
	if level != AlertError {
		t.Errorf("ErrorCount > maxRecentErrors: expected AlertError, got %s", level)
	}
}

func TestGetAlertLevel_TwoRecentRestarts_ZeroErrors_ReturnsError(t *testing.T) {
	t.Parallel()

	// recentRestartThreshold = 2; exactly at threshold → AlertError.
	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: recentRestartThreshold,
		ErrorCount:     0,
	}
	level := s.GetAlertLevel()
	if level != AlertError {
		t.Errorf("RecentRestarts=%d (==threshold): expected AlertError, got %s", recentRestartThreshold, level)
	}
}

func TestGetAlertLevel_HighRestarts_HighErrors_ReturnsErrorNotPanic(t *testing.T) {
	t.Parallel()

	// Both fields at extreme values — must not panic or overflow.
	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: 1<<31 - 1, // max int32
		ErrorCount:     1<<31 - 1,
	}
	level := s.GetAlertLevel()
	if level != AlertError {
		t.Errorf("extreme values: expected AlertError, got %s", level)
	}
}

func TestGetAlertLevel_EmptyServiceStatus_ReturnsOKNotPanic(t *testing.T) {
	t.Parallel()

	// Zero-value ServiceStatus — State is "" (not StateRunning) → Critical.
	s := ServiceStatus{}
	level := s.GetAlertLevel()
	if level != AlertCritical {
		t.Errorf("zero-value ServiceStatus (state=''): expected AlertCritical (not running), got %s", level)
	}
}

func TestGetAlertLevel_RunningZeroErrors_ReturnsInfo(t *testing.T) {
	t.Parallel()

	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: 0,
		ErrorCount:     0,
	}
	level := s.GetAlertLevel()
	if level != AlertInfo {
		t.Errorf("healthy service: expected AlertInfo, got %s", level)
	}
}

func TestGetAlertLevel_HealthcheckFailed_ReturnsError(t *testing.T) {
	t.Parallel()

	f := false
	s := ServiceStatus{
		State:         StateRunning,
		HealthcheckOK: &f,
	}
	level := s.GetAlertLevel()
	if level != AlertError {
		t.Errorf("healthcheck failed: expected AlertError, got %s", level)
	}
}

func TestGetAlertLevel_HealthcheckOK_ReturnsInfo(t *testing.T) {
	t.Parallel()

	tr := true
	s := ServiceStatus{
		State:         StateRunning,
		HealthcheckOK: &tr,
	}
	level := s.GetAlertLevel()
	if level != AlertInfo {
		t.Errorf("healthcheck ok: expected AlertInfo, got %s", level)
	}
}

func TestGetAlertLevel_NonRunningState_AlwaysCritical(t *testing.T) {
	t.Parallel()

	states := []ContainerState{StateExited, StateRestarting, StatePaused, StateDead, StateUnknown}
	for _, state := range states {
		s := ServiceStatus{State: state}
		level := s.GetAlertLevel()
		if level != AlertCritical {
			t.Errorf("state=%s: expected AlertCritical, got %s", state, level)
		}
	}
}

func TestIsHealthy_RecentRestartsBelowThreshold_IsHealthy(t *testing.T) {
	t.Parallel()

	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: recentRestartThreshold - 1,
		ErrorCount:     0,
	}
	if !s.IsHealthy() {
		t.Errorf("restarts=%d (below threshold %d): expected healthy", s.RecentRestarts, recentRestartThreshold)
	}
}

func TestIsHealthy_ErrorCountAtMax_IsHealthy(t *testing.T) {
	t.Parallel()

	// ErrorCount==maxRecentErrors is at threshold, not over — should still be healthy.
	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: 0,
		ErrorCount:     maxRecentErrors,
	}
	if !s.IsHealthy() {
		t.Errorf("ErrorCount==%d (==maxRecentErrors, not >): expected healthy", maxRecentErrors)
	}
}

func TestIsHealthy_ErrorCountOverMax_IsNotHealthy(t *testing.T) {
	t.Parallel()

	s := ServiceStatus{
		State:          StateRunning,
		RecentRestarts: 0,
		ErrorCount:     maxRecentErrors + 1,
	}
	if s.IsHealthy() {
		t.Errorf("ErrorCount > maxRecentErrors: expected unhealthy")
	}
}

// --- GenerateAlerts boundary tests ---

func testAlertConfig() Config {
	return Config{
		RestartThreshold: defaultRestartThreshold,
		ErrorThreshold:   defaultErrorThreshold,
		CPUThreshold:     defaultCPUThreshold,
		MemoryThreshold:  defaultMemoryThreshold,
	}
}

func TestGenerateAlerts_EmptyStatuses_ReturnsNil(t *testing.T) {
	t.Parallel()

	g := &AlertGenerator{cfg: testAlertConfig()}
	alerts := g.GenerateAlerts(nil)
	if len(alerts) != 0 {
		t.Errorf("empty statuses: expected 0 alerts, got %d", len(alerts))
	}
}

func TestGenerateAlerts_RunningHealthyService_ReturnsNoAlert(t *testing.T) {
	t.Parallel()

	g := &AlertGenerator{cfg: testAlertConfig()}
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, RecentRestarts: 0, ErrorCount: 0},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 0 {
		t.Errorf("healthy running service: expected 0 alerts, got %d: %v", len(alerts), alerts)
	}
}

func TestGenerateAlerts_ExitedService_ReturnsCritical(t *testing.T) {
	t.Parallel()

	g := &AlertGenerator{cfg: testAlertConfig()}
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateExited},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 1 {
		t.Fatalf("exited service: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertCritical {
		t.Errorf("exited service: expected Critical, got %s", alerts[0].Level)
	}
	if alerts[0].Service != "go-wp" {
		t.Errorf("wrong service name: %s", alerts[0].Service)
	}
}

func TestGenerateAlerts_HighRestarts_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	g := &AlertGenerator{cfg: cfg}
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, RecentRestarts: cfg.RestartThreshold},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 1 {
		t.Fatalf("high restarts: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertError {
		t.Errorf("high restarts: expected Error, got %s", alerts[0].Level)
	}
}

func TestGenerateAlerts_HighCPU_ReturnsWarning(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	g := &AlertGenerator{cfg: cfg}
	cpu := cfg.CPUThreshold + 1
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, CPUPercent: &cpu},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 1 {
		t.Fatalf("high CPU: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertWarning {
		t.Errorf("high CPU: expected Warning, got %s", alerts[0].Level)
	}
}

func TestGenerateAlerts_HighMemory_ReturnsWarning(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	g := &AlertGenerator{cfg: cfg}
	// memPct = (mem / limit) * 100 >= MemoryThreshold
	mem := 900.0
	limit := 1000.0 // 90% = exactly MemoryThreshold
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, MemoryMB: &mem, MemoryLimitMB: &limit},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 1 {
		t.Fatalf("high memory: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertWarning {
		t.Errorf("high memory: expected Warning, got %s", alerts[0].Level)
	}
}

func TestGenerateAlerts_HighErrors_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	g := &AlertGenerator{cfg: cfg}
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, ErrorCount: cfg.ErrorThreshold},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 1 {
		t.Fatalf("high errors: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertError {
		t.Errorf("high errors: expected Error, got %s", alerts[0].Level)
	}
}

func TestGenerateAlerts_HealthcheckFailed_ReturnsError(t *testing.T) {
	t.Parallel()

	g := &AlertGenerator{cfg: testAlertConfig()}
	f := false
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, HealthcheckOK: &f, HealthcheckURL: "http://localhost/health"},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 1 {
		t.Fatalf("healthcheck failed: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertError {
		t.Errorf("healthcheck failed: expected Error, got %s", alerts[0].Level)
	}
}

func TestGenerateAlerts_MultipleServices_ProducesAlertPerService(t *testing.T) {
	t.Parallel()

	g := &AlertGenerator{cfg: testAlertConfig()}
	statuses := []ServiceStatus{
		{Name: "svc-a", State: StateExited},
		{Name: "svc-b", State: StateRunning, RecentRestarts: 0, ErrorCount: 0},
		{Name: "svc-c", State: StateDead},
	}
	alerts := g.GenerateAlerts(statuses)
	// svc-a (critical) + svc-c (critical) = 2 alerts.
	if len(alerts) != 2 {
		t.Errorf("expected 2 alerts for 2 failing services, got %d: %v", len(alerts), alerts)
	}
}

func TestGenerateAlerts_MemoryLimitZero_NoAlert(t *testing.T) {
	t.Parallel()

	// MemoryLimitMB = 0 means unlimited — should not generate memory alert (division by zero guard).
	g := &AlertGenerator{cfg: testAlertConfig()}
	mem := 900.0
	limit := 0.0
	statuses := []ServiceStatus{
		{Name: "go-wp", State: StateRunning, MemoryMB: &mem, MemoryLimitMB: &limit},
	}
	alerts := g.GenerateAlerts(statuses)
	if len(alerts) != 0 {
		t.Errorf("zero memory limit: expected no alert, got %d: %v", len(alerts), alerts)
	}
}

// --- GenerateDiskAlerts boundary tests ---

func TestGenerateDiskAlerts_Empty_ReturnsNil(t *testing.T) {
	t.Parallel()

	alerts := GenerateDiskAlerts(nil, testAlertConfig())
	if len(alerts) != 0 {
		t.Errorf("empty pressures: expected 0 alerts, got %d", len(alerts))
	}
}

func TestGenerateDiskAlerts_BelowThreshold_ReturnsNoAlert(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	cfg.DiskThreshold = defaultDiskThreshold
	cfg.DiskCritical = defaultDiskCritical
	pressures := []DiskPressure{
		{MountPoint: "/", UsedPct: cfg.DiskThreshold - 1, AvailGB: 100},
	}
	alerts := GenerateDiskAlerts(pressures, cfg)
	if len(alerts) != 0 {
		t.Errorf("below threshold: expected 0 alerts, got %d", len(alerts))
	}
}

func TestGenerateDiskAlerts_AboveThreshold_ReturnsWarning(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	cfg.DiskThreshold = defaultDiskThreshold
	cfg.DiskCritical = defaultDiskCritical
	pressures := []DiskPressure{
		{MountPoint: "/", UsedPct: cfg.DiskThreshold + 1, AvailGB: 20},
	}
	alerts := GenerateDiskAlerts(pressures, cfg)
	if len(alerts) != 1 {
		t.Fatalf("above threshold: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertWarning {
		t.Errorf("above threshold (not critical): expected Warning, got %s", alerts[0].Level)
	}
}

func TestGenerateDiskAlerts_AboveCritical_ReturnsCritical(t *testing.T) {
	t.Parallel()

	cfg := testAlertConfig()
	cfg.DiskThreshold = defaultDiskThreshold
	cfg.DiskCritical = defaultDiskCritical
	pressures := []DiskPressure{
		{MountPoint: "/", UsedPct: cfg.DiskCritical + 0.5, AvailGB: 1},
	}
	alerts := GenerateDiskAlerts(pressures, cfg)
	if len(alerts) != 1 {
		t.Fatalf("above critical: expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Level != AlertCritical {
		t.Errorf("above critical threshold: expected Critical, got %s", alerts[0].Level)
	}
}
