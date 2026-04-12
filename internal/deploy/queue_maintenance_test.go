package deploy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// setMaintenanceDelays overrides maintenance timing vars for fast tests and returns a restore func.
func setMaintenanceDelays(t *testing.T, poll, maxWait time.Duration) func() {
	t.Helper()
	origPoll := maintenancePollInterval
	origMax := maintenanceMaxWait
	maintenancePollInterval = poll
	maintenanceMaxWait = maxWait
	return func() {
		maintenancePollInterval = origPoll
		maintenanceMaxWait = origMax
	}
}

func TestWaitForMaintenanceLock_NoLock(t *testing.T) {
	// Ensure no stray lock file exists.
	os.Remove(maintenanceLockPath)
	err := waitForMaintenanceLock(context.Background(), []string{"svc"})
	if err != nil {
		t.Fatalf("expected nil when no lock file, got: %v", err)
	}
}

func TestWaitForMaintenanceLock_ReleasedMidWait(t *testing.T) {
	defer setMaintenanceDelays(t, 20*time.Millisecond, 5*time.Second)()

	f, err := os.Create(maintenanceLockPath)
	if err != nil {
		t.Fatalf("could not create lock file: %v", err)
	}
	f.Close()
	defer os.Remove(maintenanceLockPath)

	// Remove the lock after two poll intervals.
	go func() {
		time.Sleep(50 * time.Millisecond)
		os.Remove(maintenanceLockPath)
	}()

	err = waitForMaintenanceLock(context.Background(), []string{"svc"})
	if err != nil {
		t.Fatalf("expected nil after lock released, got: %v", err)
	}
}

func TestWaitForMaintenanceLock_DeadlineExceeded(t *testing.T) {
	defer setMaintenanceDelays(t, 10*time.Millisecond, 30*time.Millisecond)()

	f, err := os.Create(maintenanceLockPath)
	if err != nil {
		t.Fatalf("could not create lock file: %v", err)
	}
	f.Close()
	defer os.Remove(maintenanceLockPath)

	err = waitForMaintenanceLock(context.Background(), []string{"svc"})
	if err == nil {
		t.Fatal("expected error when deadline exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "not released after") {
		t.Errorf("expected 'not released after' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "locked by: unknown") {
		t.Errorf("expected 'locked by: unknown' for empty lock file, got: %v", err)
	}
}

func TestWaitForMaintenanceLock_MetadataInError(t *testing.T) {
	defer setMaintenanceDelays(t, 10*time.Millisecond, 30*time.Millisecond)()

	if err := os.WriteFile(maintenanceLockPath, []byte("krolik: MemDB Phase 2 migration"), 0o644); err != nil {
		t.Fatalf("could not create lock file: %v", err)
	}
	defer os.Remove(maintenanceLockPath)

	err := waitForMaintenanceLock(context.Background(), []string{"svc"})
	if err == nil {
		t.Fatal("expected error when deadline exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "locked by: krolik: MemDB Phase 2 migration") {
		t.Errorf("expected lock metadata in error, got: %v", err)
	}
}

func TestWaitForMaintenanceLock_ContextCancelled(t *testing.T) {
	defer setMaintenanceDelays(t, 10*time.Millisecond, 5*time.Second)()

	f, err := os.Create(maintenanceLockPath)
	if err != nil {
		t.Fatalf("could not create lock file: %v", err)
	}
	f.Close()
	defer os.Remove(maintenanceLockPath)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()

	err = waitForMaintenanceLock(ctx, []string{"svc"})
	if err == nil {
		t.Fatal("expected error on context cancel, got nil")
	}
	if !strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("expected 'context cancelled' in error, got: %v", err)
	}
}
