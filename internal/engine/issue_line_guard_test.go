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

	// Walk the WHOLE module, not just internal/engine: a canonical issue-line
	// emitter (e.g. checkSystemdServices in cmd/dozor/gateway_watch.go) lives
	// outside this package, and a scope limited to *.go here would let such a
	// producer drift back to a hand-rolled format unnoticed.
	root := moduleRoot(t)
	for _, f := range collectGoSources(t, root) {
		src, err := os.ReadFile(f) //nolint:gosec // test-local, fixed module tree
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if !emitCall.MatchString(line) {
				continue // only string-emitting calls can be unconverted producers
			}
			for _, re := range forbidden {
				if re.MatchString(line) {
					rel, _ := filepath.Rel(root, f)
					t.Errorf("%s: forbidden hand-rolled alert emitter — route through FormatIssueLine:\n\t%s",
						rel, strings.TrimSpace(line))
				}
			}
		}
	}
}

// moduleRoot walks up from the test's working directory until it finds go.mod,
// so the guard scope is independent of which package the test is invoked from.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}

// collectGoSources returns every non-test .go file under root, skipping vendor,
// testdata, and hidden directories (e.g. .git) where canonical-looking lines
// are not real emitters.
func collectGoSources(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}
