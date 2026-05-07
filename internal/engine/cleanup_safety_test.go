package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// protectedPaths is the safety blacklist — paths that must never appear in any
// cleanup function body. Checked by TestCleanupTargets_NeverTouchProtectedPaths.
var protectedPaths = []string{
	"/mnt/cargo",
	"uploads",
	"src/*/target",
	"/var/log",
	".local",
	".config",
	"/home/krolik/bin",
}

// TestCleanupTargets_NeverTouchProtectedPaths performs a static-string scan of all
// cleanup_*.go source files to assert that none reference protected paths.
// This is a safety net: if a cleanup function accidentally touches operator-visible
// data or expensive rebuild artifacts, the test fails before the code ships.
func TestCleanupTargets_NeverTouchProtectedPaths(t *testing.T) {
	t.Parallel()

	// Locate the package directory from the test binary's working directory.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	pattern := filepath.Join(wd, "cleanup_*.go")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no cleanup_*.go files found under %s", wd)
	}

	fset := token.NewFileSet()
	for _, file := range files {
		// Skip test files — safety check is for production code only.
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

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
			val := strings.Trim(lit.Value, `"` + "`")
			for _, path := range protectedPaths {
				// src/*/target is a glob; match component-by-component.
				if path == "src/*/target" {
					if strings.Contains(val, "/src/") && strings.Contains(val, "/target") {
						pos := fset.Position(lit.Pos())
						t.Errorf("%s:%d: cleanup code references protected path pattern %q in literal %q",
							pos.Filename, pos.Line, path, val)
					}
					continue
				}
				if strings.Contains(val, path) {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d: cleanup code references protected path %q in literal %q",
						pos.Filename, pos.Line, path, val)
				}
			}
			return true
		})
	}
}
