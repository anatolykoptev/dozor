package deploy

import (
	"testing"
	"time"
)

// TestResolveCanaryTimeout_PerRepoWins verifies that an explicit per-repo
// canary_smoke_timeout overrides the per-profile default.
func TestResolveCanaryTimeout_PerRepoWins(t *testing.T) {
	cfg := RepoConfig{
		Profile:            "rust",
		CanarySmokeTimeout: Duration{D: 60 * time.Second},
	}
	got, src := resolveCanaryTimeout(cfg)
	if got != 60*time.Second {
		t.Errorf("got %v, want 60s (per-repo should win)", got)
	}
	if src != canaryTimeoutSrcPerRepo {
		t.Errorf("source = %q, want %q", src, canaryTimeoutSrcPerRepo)
	}
}

// TestResolveCanaryTimeout_RustProfileDefault verifies that the rust profile
// provides a 120s default when no per-repo timeout is set.
func TestResolveCanaryTimeout_RustProfileDefault(t *testing.T) {
	cfg := RepoConfig{
		Profile: "rust",
	}
	got, src := resolveCanaryTimeout(cfg)
	if got != 120*time.Second {
		t.Errorf("got %v, want 120s (rust profile default)", got)
	}
	if src != canaryTimeoutSrcPerProfile {
		t.Errorf("source = %q, want %q", src, canaryTimeoutSrcPerProfile)
	}
}

// TestResolveCanaryTimeout_GoFlatProfileDefault verifies that go-flat profile
// keeps the 30s default (same as the hard fallback — fast Go startup).
func TestResolveCanaryTimeout_GoFlatProfileDefault(t *testing.T) {
	cfg := RepoConfig{
		Profile: "go-flat",
	}
	got, src := resolveCanaryTimeout(cfg)
	if got != 30*time.Second {
		t.Errorf("got %v, want 30s (go-flat profile default)", got)
	}
	if src != canaryTimeoutSrcPerProfile {
		t.Errorf("source = %q, want %q", src, canaryTimeoutSrcPerProfile)
	}
}

// TestResolveCanaryTimeout_GoCmdProfileDefault verifies that go-cmd profile
// also uses 30s default.
func TestResolveCanaryTimeout_GoCmdProfileDefault(t *testing.T) {
	cfg := RepoConfig{
		Profile: "go-cmd",
	}
	got, src := resolveCanaryTimeout(cfg)
	if got != 30*time.Second {
		t.Errorf("got %v, want 30s (go-cmd profile default)", got)
	}
	if src != canaryTimeoutSrcPerProfile {
		t.Errorf("source = %q, want %q", src, canaryTimeoutSrcPerProfile)
	}
}

// TestResolveCanaryTimeout_HardFallback verifies that an empty / unknown
// profile falls back to the hard-coded 30s constant.
func TestResolveCanaryTimeout_HardFallback(t *testing.T) {
	for _, profile := range []string{"", "unknown-profile"} {
		cfg := RepoConfig{
			Profile: profile,
		}
		got, src := resolveCanaryTimeout(cfg)
		if got != 30*time.Second {
			t.Errorf("profile=%q: got %v, want 30s (hard fallback)", profile, got)
		}
		if src != canaryTimeoutSrcHardFallback {
			t.Errorf("profile=%q: source = %q, want %q", profile, src, canaryTimeoutSrcHardFallback)
		}
	}
}
