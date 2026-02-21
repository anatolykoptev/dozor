package engine

import (
	"testing"
	"time"
)

func TestDevModeToggle(t *testing.T) {
	a := &ServerAgent{}
	if a.IsDevMode() {
		t.Fatal("dev mode should be off by default")
	}
	a.SetDevMode(true)
	if !a.IsDevMode() {
		t.Fatal("dev mode should be on after SetDevMode(true)")
	}
	a.SetDevMode(false)
	if a.IsDevMode() {
		t.Fatal("dev mode should be off after SetDevMode(false)")
	}
}

func TestExclusionBasic(t *testing.T) {
	a := &ServerAgent{}

	a.ExcludeService("go-hully", 1*time.Hour)
	a.ExcludeService("memdb-api", 2*time.Hour)

	excl := a.ListExclusions()
	if len(excl) != 2 {
		t.Fatalf("expected 2 exclusions, got %d", len(excl))
	}
	if _, ok := excl["go-hully"]; !ok {
		t.Fatal("go-hully should be excluded")
	}

	a.IncludeService("go-hully")
	excl = a.ListExclusions()
	if len(excl) != 1 {
		t.Fatalf("expected 1 exclusion after include, got %d", len(excl))
	}
	if _, ok := excl["go-hully"]; ok {
		t.Fatal("go-hully should no longer be excluded")
	}
}

func TestExclusionAutoExpire(t *testing.T) {
	a := &ServerAgent{}

	// Exclude with a TTL already in the past
	a.devExclusions.Store("expired-svc", time.Now().Add(-1*time.Second))
	a.ExcludeService("active-svc", 1*time.Hour)

	excl := a.ListExclusions()
	if len(excl) != 1 {
		t.Fatalf("expected 1 active exclusion, got %d", len(excl))
	}
	if _, ok := excl["expired-svc"]; ok {
		t.Fatal("expired-svc should have been cleaned up")
	}
	if _, ok := excl["active-svc"]; !ok {
		t.Fatal("active-svc should still be excluded")
	}
}
