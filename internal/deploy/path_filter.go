package deploy

import "strings"

// MatchPath reports whether path matches at least one glob pattern.
//
// Supported syntax (intentionally minimal — enough for build_paths whitelists):
//   - "**"        matches zero or more path components (including `/`).
//   - "*"         matches any sequence within a single path component (no `/`).
//   - literal     bytes match exactly.
//
// Examples:
//
//	MatchPath("memdb-go/internal/foo.go", []string{"memdb-go/**"})  -> true
//	MatchPath("evaluation/locomo/score.py", []string{"memdb-go/**"}) -> false
//	MatchPath("ROADMAP.md",                 []string{"*.md"})         -> true
//	MatchPath("docs/sub/x.md",              []string{"*.md"})         -> false (`*` does not cross `/`)
//	MatchPath("docs/sub/x.md",              []string{"**/*.md"})      -> true
//
// An empty patterns slice yields false (caller decides default semantics).
func MatchPath(path string, patterns []string) bool {
	for _, p := range patterns {
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

// MatchAny returns true if any element of paths matches at least one pattern.
func MatchAny(paths, patterns []string) bool {
	for _, p := range paths {
		if MatchPath(p, patterns) {
			return true
		}
	}
	return false
}

// matchGlob is a backtracking matcher for the subset of glob described above.
func matchGlob(pattern, name string) bool {
	// Fast path: no wildcards.
	if !strings.ContainsAny(pattern, "*") {
		return pattern == name
	}
	return globMatch(pattern, name)
}

// globMatch implements the actual recursion. Tokens:
//   - "**" → zero-or-more characters including '/'.
//   - "*"  → zero-or-more non-'/' characters.
func globMatch(pattern, name string) bool {
	for {
		if pattern == "" {
			return name == ""
		}

		// Detect "**" (possibly followed by '/').
		if strings.HasPrefix(pattern, "**") {
			rest := pattern[2:]
			// Strip an optional leading '/' so "**/x" works at any depth, including 0.
			rest = strings.TrimPrefix(rest, "/")
			// Try every possible split — including the empty tail.
			for i := 0; i <= len(name); i++ {
				if globMatch(rest, name[i:]) {
					return true
				}
			}
			return false
		}

		// Single '*' — match within the current component.
		if pattern[0] == '*' {
			rest := pattern[1:]
			for i := 0; i <= len(name); i++ {
				// Stop at directory separator: '*' does not cross '/'.
				if i > 0 && name[i-1] == '/' {
					break
				}
				if globMatch(rest, name[i:]) {
					return true
				}
			}
			return false
		}

		// Literal byte.
		if name == "" || pattern[0] != name[0] {
			return false
		}
		pattern = pattern[1:]
		name = name[1:]
	}
}
