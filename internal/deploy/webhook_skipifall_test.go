package deploy

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestHandler_PathFilter_SkipIfAll exercises the skip_if_all path filter — the
// exact dual of skip_if_any: skip this entry when EVERY build-relevant changed
// file (post-SkipPaths subtraction) matches at least one SkipIfAll glob.
//
// Load-bearing detail pinned here: SkipIfAll evaluates on `relevant` (after
// SkipPaths subtraction), NOT the raw changed set. A push of web/foo.ts +
// README.md where *.md is a skip path is a pure-web push and must skip.
func TestHandler_PathFilter_SkipIfAll(t *testing.T) {
	t.Parallel()

	const repo = "anatolykoptev/oxpulse-chat"
	const svc = "oxpulse-chat-stagingprod"
	const sha = "abc1234567890"

	type shape struct {
		name       string
		skipIfAll  []string
		buildPaths []string
		skipPaths  []string
		skipIfAny  []string
		files      []string
		// noCommits true → elided diff (force push / oversize).
		noCommits  bool
		wantSkip   bool
		wantReason string
		// wantSkipIfAllMetric: expected delta on SkippedTotal{reason="skip_if_all"}.
		wantSkipIfAllMetric float64
	}

	shapes := []shape{
		{
			name:                "all_files_under_web_skips",
			skipIfAll:           []string{"web/**"},
			buildPaths:          []string{"web/**", "crates/**", "Cargo.toml"},
			files:               []string{"web/src/lib/foo.ts", "web/static/sw.js"},
			wantSkip:            true,
			wantReason:          "skip_if_all",
			wantSkipIfAllMetric: 1,
		},
		{
			name:       "web_plus_crates_does_not_skip",
			skipIfAll:  []string{"web/**"},
			buildPaths: []string{"web/**", "crates/**", "Cargo.toml"},
			files:      []string{"web/src/lib/foo.ts", "crates/server/src/main.rs"},
			wantSkip:   false,
		},
		{
			// Load-bearing: README.md is consumed by skip_paths (*.md), so
			// relevant = [web/src/foo.ts] — a pure-web push → skip_if_all.
			// Evaluating over the raw set would see README.md not matching
			// web/** and wrongly fire the heavy lane.
			name:                "web_plus_readme_with_md_skip_path_skips",
			skipIfAll:           []string{"web/**"},
			buildPaths:          []string{"web/**", "crates/**", "Cargo.toml"},
			skipPaths:           []string{"*.md"},
			files:               []string{"web/src/foo.ts", "README.md"},
			wantSkip:            true,
			wantReason:          "skip_if_all",
			wantSkipIfAllMetric: 1,
		},
		{
			name:       "empty_skip_if_all_backward_compat_no_skip",
			skipIfAll:  nil,
			buildPaths: []string{"web/**", "crates/**"},
			files:      []string{"web/src/lib/foo.ts", "web/static/sw.js"},
			wantSkip:   false,
		},
		{
			// All files consumed by skip_paths → only_skip_paths wins over
			// skip_if_all (precedence: step 2 before step 3).
			name:                "only_skip_paths_files_yield_only_skip_paths_not_skip_if_all",
			skipIfAll:           []string{"web/**"},
			buildPaths:          []string{"web/**", "crates/**"},
			skipPaths:           []string{"*.md"},
			files:               []string{"README.md", "CHANGELOG.md"},
			wantSkip:            true,
			wantReason:          "only_skip_paths",
			wantSkipIfAllMetric: 0,
		},
		{
			// Elided diff (no changed files) → conservative build, no skip.
			name:       "elided_diff_does_not_skip",
			skipIfAll:  []string{"web/**"},
			buildPaths: []string{"web/**", "crates/**"},
			noCommits:  true,
			wantSkip:   false,
		},
		{
			// skip_if_any precedence: both filters would match, skip_if_any
			// fires first (hard veto, step 1 before step 3).
			name:                "skip_if_any_precedence_over_skip_if_all",
			skipIfAll:           []string{"web/**"},
			skipIfAny:           []string{"web/**"},
			buildPaths:          []string{"web/**", "crates/**"},
			files:               []string{"web/src/lib/foo.ts"},
			wantSkip:            true,
			wantReason:          "skip_if_any",
			wantSkipIfAllMetric: 0,
		},
		{
			// Vacuous-truth guard: an entry configured with ONLY skip_if_all
			// (no build_paths) and a non-matching file → must not skip, and
			// must not early-return before the filter runs.
			name:      "skip_if_all_only_no_build_paths_partial_match_builds",
			skipIfAll: []string{"web/**"},
			files:     []string{"crates/server/src/main.rs"},
			wantSkip:  false,
		},
		{
			// skip_if_all only (no build_paths), all files match → skip.
			name:                "skip_if_all_only_no_build_paths_all_match_skips",
			skipIfAll:           []string{"web/**"},
			files:               []string{"web/src/lib/foo.ts", "web/static/sw.js"},
			wantSkip:            true,
			wantReason:          "skip_if_all",
			wantSkipIfAllMetric: 1,
		},
	}

	for _, tc := range shapes {
		t.Run(tc.name, func(t *testing.T) {
			// No t.Parallel() on subtests: they share the global
			// SkippedTotal{reason="skip_if_all"} counter with identical
			// (repo, svc) labels, so parallel before/delta snapshots would
			// race. No other test writes the "skip_if_all" label, so
			// sequential subtests give exact deltas.
			cfg := &Config{
				Repos: map[string]RepoConfig{
					repo + "#stagingprod": {
						ComposePath: "/tmp",
						SourcePath:  "/tmp",
						Services:    []string{svc},
						BuildPaths:  tc.buildPaths,
						SkipPaths:   tc.skipPaths,
						SkipIfAny:   tc.skipIfAny,
						SkipIfAll:   tc.skipIfAll,
					},
				},
			}
			q, _ := newTestQueue()
			h := NewHandler(cfg, q, func(string) {})
			defer h.Close()

			var body string
			if tc.noCommits {
				body = pushPayload(repo, "refs/heads/main", sha)
			} else {
				body = pushPayloadWithFiles(repo, "refs/heads/main", sha, tc.files)
			}

			before := testutil.ToFloat64(SkippedTotal.WithLabelValues(repo, svc, "skip_if_all"))

			w := postPush(h, body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var resp map[string]string
			_ = json.NewDecoder(w.Body).Decode(&resp)

			if tc.wantSkip {
				if resp["status"] != "skipped" {
					t.Fatalf("status = %q, want skipped (reason=%q)", resp["status"], tc.wantReason)
				}
				if resp["reason"] != tc.wantReason {
					t.Fatalf("reason = %q, want %q", resp["reason"], tc.wantReason)
				}
			} else if resp["status"] == "skipped" {
				t.Fatalf("status = skipped reason=%q, want not skipped", resp["reason"])
			}

			delta := testutil.ToFloat64(SkippedTotal.WithLabelValues(repo, svc, "skip_if_all")) - before
			if delta != tc.wantSkipIfAllMetric {
				t.Errorf("SkippedTotal{reason=skip_if_all} delta = %v, want %v", delta, tc.wantSkipIfAllMetric)
			}
		})
	}
}
