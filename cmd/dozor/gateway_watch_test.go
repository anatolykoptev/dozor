package main

import (
	"strings"
	"testing"
)

func TestBuildWatchPrompt_ProductionUsesHTML(t *testing.T) {
	got := buildWatchPrompt(false)

	wantSubstrings := []string{
		"<b>Status:</b>",
		"<b>Issues:</b>",
		"<b>Action:</b>",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("buildWatchPrompt(false) missing %q\nfull prompt:\n%s", want, got)
		}
	}

	mdMarkers := []string{"**Status:**", "**Issues:**", "**Action:**"}
	for _, md := range mdMarkers {
		if strings.Contains(got, md) {
			t.Errorf("buildWatchPrompt(false) still contains markdown %q — should be HTML", md)
		}
	}
}

func TestBuildWatchPrompt_DevModeUnchanged(t *testing.T) {
	got := buildWatchPrompt(true)
	if !strings.Contains(got, "DEV MODE") {
		t.Errorf("buildWatchPrompt(true) missing DEV MODE marker; got: %s", got)
	}
}
