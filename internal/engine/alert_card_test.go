package engine

import (
	"testing"
	"time"
)

func TestSatoriTimeout_DefaultWhenUnset(t *testing.T) {
	t.Setenv("DOZOR_SATORI_TIMEOUT", "")
	if got := satoriTimeout(); got != 10*time.Second {
		t.Errorf("expected 10s default, got %v", got)
	}
}

func TestSatoriTimeout_RespectsValid(t *testing.T) {
	t.Setenv("DOZOR_SATORI_TIMEOUT", "15s")
	if got := satoriTimeout(); got != 15*time.Second {
		t.Errorf("expected 15s, got %v", got)
	}
}

func TestSatoriTimeout_FallbackOnInvalid(t *testing.T) {
	t.Setenv("DOZOR_SATORI_TIMEOUT", "not-a-duration")
	if got := satoriTimeout(); got != 10*time.Second {
		t.Errorf("expected 10s default for invalid env, got %v", got)
	}
}

func TestSatoriTimeout_FallbackOnZero(t *testing.T) {
	t.Setenv("DOZOR_SATORI_TIMEOUT", "0s")
	if got := satoriTimeout(); got != 10*time.Second {
		t.Errorf("expected 10s default for zero duration, got %v", got)
	}
}
