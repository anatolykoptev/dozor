package main

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

func TestMapTriageLevelToAlertLevel_Warning(t *testing.T) {
	t.Parallel()

	level := mapTriageLevelToAlertLevel("WARNING")
	if level != engine.AlertWarning {
		t.Errorf("expected AlertWarning, got %s", level)
	}
}

func TestMapTriageLevelToAlertLevel_Critical(t *testing.T) {
	t.Parallel()

	level := mapTriageLevelToAlertLevel("CRITICAL")
	if level != engine.AlertCritical {
		t.Errorf("expected AlertCritical, got %s", level)
	}
}

func TestMapTriageLevelToAlertLevel_Error(t *testing.T) {
	t.Parallel()

	level := mapTriageLevelToAlertLevel("ERROR")
	if level != engine.AlertCritical {
		t.Errorf("ERROR should map to AlertCritical, got %s", level)
	}
}

func TestMapTriageLevelToAlertLevel_WarningHigh(t *testing.T) {
	t.Parallel()

	level := mapTriageLevelToAlertLevel("WARNING_HIGH")
	if level != engine.AlertWarningHigh {
		t.Errorf("expected AlertWarningHigh, got %s", level)
	}
}

func TestMapTriageLevelToAlertLevel_Unknown(t *testing.T) {
	t.Parallel()

	level := mapTriageLevelToAlertLevel("BOGUS")
	if level != engine.AlertInfo {
		t.Errorf("unknown level should map to AlertInfo, got %s", level)
	}
}

func TestBuildAutoRemediateMessage_WithDisks(t *testing.T) {
	t.Parallel()

	msg := buildAutoRemediateMessage(nil, nil, []string{"/mnt/cargo: journal=120MB, caches=400MB"})
	if !strings.Contains(msg, "Auto-fix applied") {
		t.Error("missing header")
	}
	if !strings.Contains(msg, "Disk freed") {
		t.Errorf("expected 'Disk freed' section, got: %s", msg)
	}
	if !strings.Contains(msg, "/mnt/cargo") {
		t.Errorf("expected disk entry in message, got: %s", msg)
	}
}

func TestBuildAutoRemediateMessage_NilDisks_BackwardCompatible(t *testing.T) {
	t.Parallel()

	// Existing callers pass nil disks — must not panic or add empty section.
	msg := buildAutoRemediateMessage([]string{"ox-whisper"}, nil, nil)
	if strings.Contains(msg, "Disk freed") {
		t.Errorf("nil disks: should not include Disk freed section, got: %s", msg)
	}
	if !strings.Contains(msg, "ox-whisper") {
		t.Error("missing restarted service")
	}
}
