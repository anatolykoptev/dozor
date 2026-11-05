package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anatolykoptev/dozor/internal/engine"
)

func TestBuildAlertCaption_GroupListsEveryMember(t *testing.T) {
	alerts := []engine.Alert{
		{Level: engine.AlertCritical, Service: "HighErrorRate", Title: "5xx above 5%"},
		{Level: engine.AlertWarning, Service: "DiskFilling", Title: "/ at 91%"},
		{Level: engine.AlertInfo, Service: "BackupLate", Title: "[RESOLVED] backup late"},
	}
	// A fallback that alone would blow the budget on member 1 — the old path
	// truncated it and hid the rest.
	fallback := buildAlertmanagerFallback(withLongDesc(alerts))
	got := buildAlertCaption(alerts, fallback)
	for _, a := range alerts {
		if !strings.Contains(got, a.Service) {
			t.Errorf("caption hides %q:\n%s", a.Service, got)
		}
	}
	if utf8.RuneCountInString(got) > engine.MaxCaptionRunes {
		t.Errorf("caption %d runes exceeds %d", utf8.RuneCountInString(got), engine.MaxCaptionRunes)
	}
}

func TestBuildAlertCaption_OverflowStatesHiddenCount(t *testing.T) {
	var alerts []engine.Alert
	for i := range 200 {
		alerts = append(alerts, engine.Alert{Level: engine.AlertWarning, Service: fmt.Sprintf("Alert%03d", i), Title: strings.Repeat("x", 90)})
	}
	got := buildAlertCaption(alerts, "")
	if n := utf8.RuneCountInString(got); n > engine.MaxCaptionRunes {
		t.Fatalf("caption %d runes exceeds %d", n, engine.MaxCaptionRunes)
	}
	if !strings.HasPrefix(got, "200 alerts in group:") {
		t.Errorf("caption must state the group size: %q", got[:40])
	}
	m := regexp.MustCompile(`\(\+(\d+) more\)`).FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("overflow must state how many members are not listed:\n%s", got)
	}
	shown := strings.Count(got, "Alert")
	hidden, _ := strconv.Atoi(m[1])
	if shown+hidden < len(alerts) {
		t.Errorf("shown %d + hidden %d < %d: members unaccounted for", shown, hidden, len(alerts))
	}
}

func TestBuildAlertCaption_SingleKeepsFallback(t *testing.T) {
	a := []engine.Alert{{Level: engine.AlertCritical, Service: "X", Title: "t", Description: "full description"}}
	if got := buildAlertCaption(a, "[CRITICAL] t\nfull description"); !strings.Contains(got, "full description") {
		t.Errorf("single alert lost its description: %q", got)
	}
}

func withLongDesc(in []engine.Alert) []engine.Alert {
	out := append([]engine.Alert(nil), in...)
	for i := range out {
		out[i].Description = strings.Repeat("d", 500)
	}
	return out
}
