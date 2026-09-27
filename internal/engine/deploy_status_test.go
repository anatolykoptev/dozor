package engine

import (
	"context"
	"testing"
)

// Issue #201: server_deploy advertised a deploy_id whose status check could
// never answer RUNNING — the manual deploy is a goroutine inside dozor, so
// pgrep -f <id> finds nothing and the log file stayed empty for the whole
// build. The registry makes the status truthful from the first instant.
func TestGetDeployStatus_ManualDeployRegistry(t *testing.T) {
	a := &ServerAgent{transport: NewTransport(Config{})}
	id := "deploy-manual-test-xyz"

	setManualDeployStatus(id, manualDeployRunning)
	t.Cleanup(func() {
		manualDeployMu.Lock()
		delete(manualDeployStates, id)
		manualDeployMu.Unlock()
	})

	st := a.GetDeployStatus(context.Background(), id)
	if st.Status != "RUNNING" {
		t.Fatalf("status = %q, want RUNNING (registered manual deploy)", st.Status)
	}
	if !st.ProcessRunning {
		t.Fatal("ProcessRunning must be true for a RUNNING manual deploy")
	}

	setManualDeployStatus(id, manualDeployCompleted)
	st = a.GetDeployStatus(context.Background(), id)
	if st.Status != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED", st.Status)
	}
	if st.ProcessRunning {
		t.Fatal("ProcessRunning must be false once the deploy completed")
	}
}

// Issue #201 continuation: the status path validates the ID before consulting
// anything — ^deploy-\d+$ rejected the deploy-manual-<ts> IDs that
// StartManualDeploy mints and advertises, making the registry unreachable.
func TestValidateDeployID_AcceptsManualIDs(t *testing.T) {
	if ok, reason := ValidateDeployID("deploy-manual-1700000000"); !ok {
		t.Fatalf("deploy-manual ID rejected: %s", reason)
	}
	if ok, _ := ValidateDeployID("deploy-1700000000"); !ok {
		t.Fatal("legacy deploy ID must still validate")
	}
	if ok, _ := ValidateDeployID("deploy-evil-1700000000"); ok {
		t.Fatal("arbitrary word IDs must still be rejected")
	}
}
