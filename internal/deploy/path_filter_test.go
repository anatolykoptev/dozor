package deploy

import "testing"

func TestMatchPath(t *testing.T) {
	t.Parallel()

	buildPaths := []string{
		"memdb-go/**",
		"go.mod",
		"go.sum",
		"Dockerfile",
		"docker/**",
	}

	tests := []struct {
		name     string
		path     string
		patterns []string
		want     bool
	}{
		// Spec-required cases.
		{"deep memdb-go file", "memdb-go/internal/handlers/foo.go", buildPaths, true},
		{"evaluation skip", "evaluation/locomo/score.py", buildPaths, false},
		{"top-level markdown", "ROADMAP.md", buildPaths, false},

		// Top-level whitelisted files.
		{"go.mod literal", "go.mod", buildPaths, true},
		{"Dockerfile literal", "Dockerfile", buildPaths, true},
		{"docker subdir", "docker/builder/Dockerfile", buildPaths, true},

		// Empty patterns → never matches.
		{"empty patterns", "anything", nil, false},

		// Single-component '*' should not cross '/'.
		{"star single component", "docs/x.md", []string{"*.md"}, false},
		{"star matches in dir", "x.md", []string{"*.md"}, true},

		// '**' in the middle.
		{"double star middle", "docs/sub/x.md", []string{"**/*.md"}, true},
		{"double star middle no nest", "x.md", []string{"**/*.md"}, true},

		// Trailing '**'.
		{"trailing double star", "memdb-go/", []string{"memdb-go/**"}, true},
		// "memdb-go" with no trailing slash is not under the dir; doublestar excludes it.
		{"trailing double star bare dir", "memdb-go", []string{"memdb-go/**"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MatchPath(tt.path, tt.patterns)
			if got != tt.want {
				t.Errorf("MatchPath(%q, %v) = %v, want %v",
					tt.path, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestMatchAny(t *testing.T) {
	t.Parallel()

	patterns := []string{"memdb-go/**", "*.md"}

	if !MatchAny([]string{"docs/foo.go", "memdb-go/x.go"}, patterns) {
		t.Error("expected match: memdb-go/x.go")
	}
	if MatchAny([]string{"docs/foo.go", "evaluation/x.py"}, patterns) {
		t.Error("unexpected match")
	}
	if MatchAny(nil, patterns) {
		t.Error("nil paths should not match")
	}
}
