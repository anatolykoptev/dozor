package engine

import (
	"sync"
	"testing"
)

func TestFailureTracker_Threshold(t *testing.T) {
	ft := NewFailureTracker(3)

	confirmed, count := ft.RecordFailure("svc1")
	if confirmed || count != 1 {
		t.Fatalf("expected not confirmed, count=1; got confirmed=%v count=%d", confirmed, count)
	}

	confirmed, count = ft.RecordFailure("svc1")
	if confirmed || count != 2 {
		t.Fatalf("expected not confirmed, count=2; got confirmed=%v count=%d", confirmed, count)
	}

	confirmed, count = ft.RecordFailure("svc1")
	if !confirmed || count != 3 {
		t.Fatalf("expected confirmed, count=3; got confirmed=%v count=%d", confirmed, count)
	}

	// Beyond threshold still confirmed.
	confirmed, count = ft.RecordFailure("svc1")
	if !confirmed || count != 4 {
		t.Fatalf("expected confirmed, count=4; got confirmed=%v count=%d", confirmed, count)
	}
}

func TestFailureTracker_ResetOnSuccess(t *testing.T) {
	ft := NewFailureTracker(2)

	ft.RecordFailure("svc1")
	ft.RecordSuccess("svc1")

	confirmed, count := ft.RecordFailure("svc1")
	if confirmed || count != 1 {
		t.Fatalf("expected reset after success; got confirmed=%v count=%d", confirmed, count)
	}
}

func TestFailureTracker_IndependentKeys(t *testing.T) {
	ft := NewFailureTracker(2)

	ft.RecordFailure("svc1")
	ft.RecordFailure("svc2")

	confirmed1, _ := ft.RecordFailure("svc1")
	confirmed2, count2 := ft.RecordFailure("svc2")

	if !confirmed1 {
		t.Fatal("svc1 should be confirmed at count=2")
	}
	if !confirmed2 || count2 != 2 {
		t.Fatal("svc2 should be confirmed at count=2")
	}
}

func TestFailureTracker_Concurrent(t *testing.T) {
	ft := NewFailureTracker(100)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ft.RecordFailure("svc1")
		}()
	}
	wg.Wait()

	confirmed, count := ft.RecordFailure("svc1")
	if !confirmed || count != 101 {
		t.Fatalf("expected count=101; got confirmed=%v count=%d", confirmed, count)
	}
}

func TestFailureTracker_MinThreshold(t *testing.T) {
	ft := NewFailureTracker(0) // should clamp to 1
	confirmed, _ := ft.RecordFailure("svc1")
	if !confirmed {
		t.Fatal("threshold=0 should clamp to 1, first failure confirms")
	}
}

func TestFlapDetector_SteadyState(t *testing.T) {
	fd := NewFlapDetector(10, 0.7, 0.3)

	// All failures — no oscillation, not flapping.
	for i := 0; i < 10; i++ {
		status := fd.Record("svc1", false)
		if status.Flapping {
			t.Fatalf("steady failures should not be flapping at iteration %d", i)
		}
	}
}

func TestFlapDetector_Oscillating(t *testing.T) {
	fd := NewFlapDetector(10, 0.7, 0.3)

	// Alternate true/false — every transition is a change = 100% rate.
	var lastStatus FlapStatus
	for i := 0; i < 10; i++ {
		lastStatus = fd.Record("svc1", i%2 == 0)
	}
	if !lastStatus.Flapping {
		t.Fatalf("alternating results should be flapping; rate=%.2f", lastStatus.ChangeRate)
	}
}

func TestFlapDetector_RecoveryFromFlapping(t *testing.T) {
	fd := NewFlapDetector(6, 0.7, 0.3)

	// Create flapping state.
	for i := 0; i < 6; i++ {
		fd.Record("svc1", i%2 == 0)
	}

	// Now stabilize with all successes — change rate drops.
	var lastStatus FlapStatus
	for i := 0; i < 6; i++ {
		lastStatus = fd.Record("svc1", true)
	}

	if lastStatus.Flapping {
		t.Fatalf("should have stopped flapping after stabilizing; rate=%.2f", lastStatus.ChangeRate)
	}
}

func TestFlapDetector_MinWindowSize(t *testing.T) {
	fd := NewFlapDetector(1, 0.7, 0.3) // should clamp to 3
	fd.Record("svc1", true)
	fd.Record("svc1", false)
	status := fd.Record("svc1", true) // 3rd sample enables detection
	// With alternating in 3 samples: changes=2, rate=1.0 -> flapping.
	if !status.Flapping {
		t.Fatalf("expected flapping with 3 alternating samples; rate=%.2f", status.ChangeRate)
	}
}

func TestAlertKey(t *testing.T) {
	a := Alert{Level: AlertCritical, Service: "redis", Title: "redis is exited"}
	key := AlertKey(a)
	expected := "critical:redis:redis is exited"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}
