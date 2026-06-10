package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoUnconvertedAlertEmitters is the regression guard for the unified-alert
// refactor: every machine-readable `[LEVEL] service — desc` line MUST be
// written through FormatIssueLine, and the old non-canonical LLM shape
// (`- [LEVEL] title: desc`, invisible to ExtractIssues) must not reappear.
//
// It greps the engine package source for the forbidden literal emitter
// patterns. FormatIssueLine itself is allowlisted (it is the one writer). If
// this test fails, a producer drifted back to a hand-rolled format that
// ExtractIssues silently ignores — exactly the bug this refactor removed.
func TestNoUnconvertedAlertEmitters(t *testing.T) {
	t.Parallel()

	// Forbidden: a string-emitting call (Fprintf/Sprintf/WriteString) whose
	// literal bakes a canonical alert line by hand instead of calling
	// FormatIssueLine. Two shapes are caught:
	//   1. the old LLM "- [%s] title: desc" prefix (invisible to ExtractIssues), and
	//   2. a literal "[LEVEL] … — …" line carrying the TriageMachineSep em-dash —
	//      that em-dash is the canonical machine marker, so any hand-rolled line
	//      containing it is an unconverted producer.
	// FormatIssueLine assembles its output from the level token + the separator
	// and never appears as such a literal, so it is not matched. The
	// issueLevelPrefixes parser table (map keys, no emit call) and reports in
	// other domains (FormatUpdatesCheck uses "[LEVEL] x: y" with a colon, not the
	// em-dash) are correctly left alone.
	emitCall := regexp.MustCompile(`\b(Fprintf|Sprintf|WriteString)\b`)
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`"\s*-\s*\[%s\]`),                                        // old LLM "- [%s] title: desc"
		regexp.MustCompile(`"\[(CRITICAL|ERROR|WARNING_HIGH|WARNING)\][^"]*` + "—"), // literal "[LEVEL] … — …"
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue // test fixtures legitimately contain canonical lines
		}
		src, err := os.ReadFile(f) //nolint:gosec // test-local, fixed glob
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if !emitCall.MatchString(line) {
				continue // only string-emitting calls can be unconverted producers
			}
			for _, re := range forbidden {
				if re.MatchString(line) {
					t.Errorf("%s: forbidden hand-rolled alert emitter — route through FormatIssueLine:\n\t%s",
						f, strings.TrimSpace(line))
				}
			}
		}
	}
}
