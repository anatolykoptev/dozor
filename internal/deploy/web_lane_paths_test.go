package deploy

import (
	"testing"
)

func TestWebLanePathFilterMatrix(t *testing.T) {
	t.Parallel()

	buildPaths := []string{
		"web/**", "packages/url-contract/**", "packages/crypto-primitives/**",
		"scripts/**", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml",
		".npmrc", "patches/**", ".dockerignore",
	}
	skipIfAny := []string{
		"crates/**", "Cargo.toml", "Cargo.lock", "Dockerfile", "Dockerfile.web",
		"config/**", "compose/**", "assets/**", ".sqlx/**", "test-e2e/**",
	}
	skipPaths := []string{
		"web/node_modules/**", "node_modules/**", "web/.svelte-kit/**",
		"web/build/**", "*.md", "docs/**",
	}

	tests := []struct {
		name  string
		files []string
		want  string // "fire" or "skip"
	}{
		{"web-only push", []string{"web/src/lib/foo.ts", "web/static/sw.js"}, "fire"},
		{"web + Rust push", []string{"web/src/lib/foo.ts", "crates/server/src/main.rs"}, "skip"},
		{"Cargo.lock only", []string{"Cargo.lock"}, "skip"},
		{"pnpm-lock.yaml only", []string{"pnpm-lock.yaml"}, "fire"},
		{"config/spa-routes.json", []string{"config/spa-routes.json"}, "skip"},
		{"CSP template", []string{"crates/server/src/branding/csp-template.txt"}, "skip"},
		{"docs only", []string{"docs/README.md"}, "skip"},
		{"packages/crypto-primitives", []string{"packages/crypto-primitives/src/index.ts"}, "fire"},
		{"Dockerfile.web", []string{"Dockerfile.web"}, "skip"},
		{"unclassified path", []string{".github/workflows/test.yml"}, "skip"},
		{"patches only", []string{"patches/@oxpulse__wire-codec@0.4.1.patch"}, "fire"},
		{"web .svelte-kit (skip_paths)", []string{"web/.svelte-kit/types/foo.ts"}, "skip"},
		{"web + pnpm-lock", []string{"web/src/lib/foo.ts", "pnpm-lock.yaml"}, "fire"},
		{"scripts only", []string{"scripts/ensure-built-deps.mjs"}, "fire"},
		{"test-e2e only", []string{"test-e2e/harness.spec.ts"}, "skip"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Step 0: SkipIfAny
			if MatchAny(tc.files, skipIfAny) {
				if tc.want != "skip" {
					t.Errorf("got skip (skip_if_any), want %s", tc.want)
				}
				return
			}

			// Step 1: Subtract SkipPaths
			relevant := []string{}
			for _, f := range tc.files {
				if !MatchPath(f, skipPaths) {
					relevant = append(relevant, f)
				}
			}
			if len(relevant) == 0 {
				if tc.want != "skip" {
					t.Errorf("got skip (only_skip_paths), want %s", tc.want)
				}
				return
			}

			// Step 2: BuildPaths
			if MatchAny(relevant, buildPaths) {
				if tc.want != "fire" {
					t.Errorf("got fire, want %s", tc.want)
				}
				return
			}

			if tc.want != "skip" {
				t.Errorf("got skip (no_relevant_paths), want %s", tc.want)
			}
		})
	}
}
