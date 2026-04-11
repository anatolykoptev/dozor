package agent

import (
	"context"
	"strings"
	"testing"
)

// Calling BuildStartupSnapshot with a nil searcher must return empty and
// must never panic — this is the "MemDB not configured" path.
func TestBuildStartupSnapshot_NilSearcher(t *testing.T) {
	got := BuildStartupSnapshot(context.Background(), nil)
	if got != "" {
		t.Errorf("expected empty snapshot for nil searcher, got %q", got)
	}
}

// formatSnapshot must wrap the raw content in the recognisable XML-like
// block and include the disambiguation comment.
func TestFormatSnapshot_Wrapping(t *testing.T) {
	raw := "- Postgres default user is memos"
	out := formatSnapshot(raw)
	if !strings.HasPrefix(out, "<startup_snapshot source=\"memdb_search\">\n") {
		t.Errorf("missing opening tag, got: %s", out)
	}
	if !strings.Contains(out, "Postgres default user is memos") {
		t.Errorf("snapshot content missing, got: %s", out)
	}
	if !strings.Contains(out, "</startup_snapshot>") {
		t.Errorf("missing closing tag, got: %s", out)
	}
	if !strings.Contains(out, "boot-time context") {
		t.Errorf("missing disambiguation comment, got: %s", out)
	}
}

func TestFormatSnapshot_TrimsTrailingNewlines(t *testing.T) {
	out := formatSnapshot("content\n\n\n")
	if strings.Contains(out, "content\n\n") {
		t.Error("formatSnapshot should trim trailing whitespace from raw input")
	}
}
