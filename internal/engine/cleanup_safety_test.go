package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// protectedPath is a blanket-forbidden literal, with an optional exemption list
// for the single file that is the DESIGNATED, disciplined owner of that resource.
// cleanup_cargo.go is the only file allowed to reference /mnt/cargo: it enforces
// its own denylist (cargoDenylist) + fail-closed structural validation
// (looksLikeCargoTarget) + age-bounded, non-recursive pruning (find -atime
// -delete, never rm -rf — see TestCleanCargo_NeverIssuesBareRmCommand in
// cleanup_cargo_test.go). Every OTHER cleanup/remediation file must still never
// reference /mnt/cargo — this preserves the original guard's intent: catching an
// ACCIDENTAL touch of the shared cargo-cache volume from an unrelated routine,
// not blocking the one deliberate, reviewed owner of it.
type protectedPath struct {
	pattern      string
	allowedFiles []string // base filenames exempt from this pattern
}

// protectedPaths is the safety blacklist — paths that must never appear in any
// cleanup function body (outside an allowedFiles exemption). Checked by
// TestCleanupTargets_NeverTouchProtectedPaths.
var protectedPaths = []protectedPath{
	{pattern: "/mnt/cargo", allowedFiles: []string{"cleanup_cargo.go"}},
	{pattern: "uploads"},
	{pattern: "src/*/target"},
	{pattern: "/var/log"},
	{pattern: ".local"},
	{pattern: ".config"},
	{pattern: "/home/krolik/bin"},
}

// isCleanupFile reports whether a .go filename belongs to cleanup/remediation
// production code that must not reference protected paths. We check both the
// classic cleanup_*.go files and any *_remediate.go or disk_*.go files where
// cleanup logic may also live (e.g. disk_remediate.go, gateway_remediate.go).
func isCleanupFile(name string) bool {
	return strings.HasPrefix(name, "cleanup_") ||
		strings.HasSuffix(name, "_remediate.go") ||
		strings.HasPrefix(name, "disk_")
}

// TestCleanupTargets_NeverTouchProtectedPaths performs a static-string scan of all
// cleanup and remediation source files in internal/engine/ and cmd/dozor/ to assert
// that none reference protected paths. Scanning both directories (not just
// internal/engine/cleanup_*.go) catches cleanup logic added in disk_remediate.go,
// gateway_remediate.go, or other remediation files.
// This is a safety net: if a cleanup function accidentally touches operator-visible
// data or expensive rebuild artifacts, the test fails before the code ships.
func TestCleanupTargets_NeverTouchProtectedPaths(t *testing.T) {
	t.Parallel()

	// Locate the package directory from the test binary's working directory.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	// Walk internal/engine/ (wd) and cmd/dozor/ for cleanup/remediation files.
	// cmd/dozor/ is two levels up from internal/engine/ in a standard Go repo
	// layout: internal/engine/ → internal/ → repo root → cmd/dozor/.
	repoRoot := filepath.Join(wd, "..", "..")
	scanDirs := []string{
		wd,
		filepath.Join(repoRoot, "cmd", "dozor"),
	}

	var files []string
	for _, dir := range scanDirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			// cmd/dozor may not exist in all test environments; skip gracefully.
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			if strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			if !isCleanupFile(e.Name()) {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}

	if len(files) == 0 {
		t.Fatalf("no cleanup/remediation .go files found under %v", scanDirs)
	}

	fset := token.NewFileSet()
	for _, file := range files {
		src, err := os.ReadFile(file) //nolint:gosec
		if err != nil {
			t.Errorf("failed to read %s: %v", file, err)
			continue
		}

		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Errorf("failed to parse %s: %v", file, err)
			continue
		}

		// Walk all string literals in function bodies.
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val := strings.Trim(lit.Value, `"`+"`")
			for _, path := range protectedPaths {
				if slices.Contains(path.allowedFiles, filepath.Base(file)) {
					continue
				}
				// src/*/target is a glob; match component-by-component.
				if path.pattern == "src/*/target" {
					if strings.Contains(val, "/src/") && strings.Contains(val, "/target") {
						pos := fset.Position(lit.Pos())
						t.Errorf("%s:%d: cleanup code references protected path pattern %q in literal %q",
							pos.Filename, pos.Line, path.pattern, val)
					}
					continue
				}
				if strings.Contains(val, path.pattern) {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d: cleanup code references protected path %q in literal %q",
						pos.Filename, pos.Line, path.pattern, val)
				}
			}
			return true
		})
	}
}
