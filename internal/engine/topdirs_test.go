package engine

import (
	"sort"
	"testing"
)

// TestTopDirs_ParsesDuOutput verifies that parseDuOutput correctly extracts
// DirSize entries from typical `du -h --max-depth=1` output.
func TestTopDirs_ParsesDuOutput(t *testing.T) {
	t.Parallel()

	raw := "5.2G\t/var\n3.1G\t/home\n800M\t/tmp\n"
	got := parseDuOutput(raw, 10)

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(got), got)
	}

	// Sort by bytes desc for deterministic assertion (parseDuOutput returns sorted).
	sort.Slice(got, func(i, j int) bool { return got[i].Bytes > got[j].Bytes })

	if got[0].Path != "/var" {
		t.Errorf("first entry should be /var, got %s", got[0].Path)
	}
	if got[0].Size != "5.2G" {
		t.Errorf("first size should be 5.2G, got %s", got[0].Size)
	}
	// 5.2G ≈ 5324.8 MB → 5324*1024*1024 bytes approximation; just check > 5GB.
	if got[0].Bytes < 5*1024*1024*1024 {
		t.Errorf("first bytes should be > 5GB, got %d", got[0].Bytes)
	}

	if got[1].Path != "/home" {
		t.Errorf("second entry should be /home, got %s", got[1].Path)
	}

	if got[2].Path != "/tmp" {
		t.Errorf("third entry should be /tmp, got %s", got[2].Path)
	}
}

// TestTopDirs_EmptyOutputNoError verifies that empty du output returns an empty slice,
// not an error.
func TestTopDirs_EmptyOutputNoError(t *testing.T) {
	t.Parallel()

	got := parseDuOutput("", 10)

	if len(got) != 0 {
		t.Errorf("expected empty slice for empty output, got %d entries", len(got))
	}
}

// TestTopDirs_TopNLimit verifies that parseDuOutput respects the n limit.
func TestTopDirs_TopNLimit(t *testing.T) {
	t.Parallel()

	raw := "5.2G\t/var\n3.1G\t/home\n800M\t/tmp\n200M\t/opt\n100M\t/boot\n50M\t/srv\n"
	got := parseDuOutput(raw, 3)

	if len(got) != 3 {
		t.Errorf("expected 3 entries (n=3), got %d", len(got))
	}
}

// TestParseDuOutput_FiltersBelow100MB verifies that entries smaller than 100 MB are
// dropped in Go after parsing, not by a GNU-only --threshold flag.
func TestParseDuOutput_FiltersBelow100MB(t *testing.T) {
	t.Parallel()

	// Mix: 5G, 200M (keep) + 50M, 1.2K (drop).
	raw := "5G\t/var\n200M\t/opt\n50M\t/tmp\n1.2K\t/boot\n"
	got := parseDuOutput(raw, 10)

	if len(got) != 2 {
		t.Fatalf("expected 2 entries (>=100MB), got %d: %+v", len(got), got)
	}
	for _, d := range got {
		if d.Bytes < 100*1024*1024 {
			t.Errorf("entry %s (%d bytes) should have been filtered out", d.Path, d.Bytes)
		}
	}
}

// TestTopDirs_LocaleC_decimalDot verifies that comma-decimal input (locale artefact)
// is gracefully ignored — ParseSizeMB returns 0 for unparseable, so the entry is
// filtered by the <100 MB threshold.
func TestTopDirs_LocaleC_decimalDot(t *testing.T) {
	t.Parallel()

	// Comma decimal — would appear on non-LC_ALL=C hosts. Graceful ignore expected.
	raw := "5,2G\t/var\n"
	got := parseDuOutput(raw, 10)

	// "5,2G" → numStr stops at comma → ParseSizeMB returns 0 → bytes=0 < 100MB → filtered.
	if len(got) != 0 {
		t.Errorf("comma-decimal entry should be filtered as unparseable (bytes=0 < 100MB), got %d entries", len(got))
	}
}

// TestTopDirs_SkipsTotalLine verifies that a "total" line emitted by du is excluded.
func TestTopDirs_SkipsTotalLine(t *testing.T) {
	t.Parallel()

	// du sometimes emits a total line like "8.2G\t." or "8.2G\ttotal"
	raw := "5.2G\t/var\n3.1G\t/home\n8.2G\t.\n"
	got := parseDuOutput(raw, 10)

	for _, d := range got {
		if d.Path == "." || d.Path == "total" {
			t.Errorf("parseDuOutput should skip '.' and 'total' lines, got: %+v", d)
		}
	}
}
