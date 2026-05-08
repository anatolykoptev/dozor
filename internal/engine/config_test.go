package engine

import (
	"testing"
)

func TestEffectiveRemoteConfirmCount_FallbackToGlobal(t *testing.T) {
	cfg := Config{AlertConfirmCount: 2, RemoteAlertConfirmCount: 0}
	if got := cfg.EffectiveRemoteConfirmCount(); got != 2 {
		t.Errorf("expected fallback to 2, got %d", got)
	}
}

func TestEffectiveRemoteConfirmCount_OverridesGlobal(t *testing.T) {
	cfg := Config{AlertConfirmCount: 2, RemoteAlertConfirmCount: 5}
	if got := cfg.EffectiveRemoteConfirmCount(); got != 5 {
		t.Errorf("expected override 5, got %d", got)
	}
}

func TestEffectiveRemoteConfirmCount_NegativeRemoteFallsBack(t *testing.T) {
	cfg := Config{AlertConfirmCount: 3, RemoteAlertConfirmCount: -1}
	if got := cfg.EffectiveRemoteConfirmCount(); got != 3 {
		t.Errorf("expected fallback for negative override, got %d", got)
	}
}
