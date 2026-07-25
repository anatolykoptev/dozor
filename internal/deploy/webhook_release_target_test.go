package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Tests for issue #169: a release event must select its deploy target(s) by
// deploy_on: release, NOT by map-iteration first-match. The previous code
// fell back to LookupBranch(repo, "") when no release-gated entry was found,
// which returns a random map entry for multi-entry repos — silently deploying
// the wrong environment (e.g. staging instead of production).

// noResolveSHA is a shaResolverFunc that always returns "unknown", so
// attachReleaseDiff's BuildPaths gating is bypassed (conservative build
// fallback) and the test focuses on target selection, not diff resolution.
func noResolveSHA(context.Context, string) string { return "unknown" }

// TestHandler_ReleaseTarget_OneReleaseOnePush_DeploysReleaseTarget is the
// core regression: a repo with a prod entry (deploy_on: release) and a
// staging entry (push-based, keyed "owner/repo#staging"). A release event
// must ALWAYS deploy the prod target, never the staging one — regardless of
// Go's randomised map iteration order.
//
// The assertion is run 50 times so that map-order randomness would surface
// against the buggy first-match fallback: with the bug, the fallback to
// LookupBranch(repo, "") would sometimes return the staging entry, enqueuing
// oxpulse-chat-staging instead of oxpulse-chat. A single-shot test could
// pass by luck against the buggy code — which is precisely how this survived.
func TestHandler_ReleaseTarget_OneReleaseOnePush_DeploysReleaseTarget(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/oxpulse-chat": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"oxpulse-chat"},
				DeployOn:    "release",
			},
			"anatolykoptev/oxpulse-chat#staging": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"oxpulse-chat-staging"},
			},
		},
	}

	for i := 0; i < 50; i++ {
		q, _ := newTestQueue()
		h := NewHandler(cfg, q, func(string) {})
		h.shaResolver = noResolveSHA

		body := releasePayload("anatolykoptev/oxpulse-chat", "v1.0.0", "main")
		w := postRelease(h, body)

		if w.Code != http.StatusOK {
			t.Fatalf("iter %d: status = %d", i, w.Code)
		}
		var resp map[string]string
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["status"] != "queued" {
			t.Fatalf("iter %d: status = %q, want queued", i, resp["status"])
		}
		if !q.queuedHas(serviceKey([]string{"oxpulse-chat"})) {
			t.Fatalf("iter %d: prod target oxpulse-chat must be enqueued", i)
		}
		if q.queuedHas(serviceKey([]string{"oxpulse-chat-staging"})) {
			t.Fatalf("iter %d: staging target must NOT be enqueued on release event — "+
				"this is the #169 bug (first-match routed the release to the wrong environment)", i)
		}
		h.Close()
	}
}

// TestHandler_ReleaseTarget_NoReleaseTarget_Ignored verifies that a release
// event for a repo with NO deploy_on: release entry is ignored — NOT
// dispatched to a random first-match. This is the exact bug: the previous
// fallback to LookupBranch(repo, "") picked a random map entry for
// multi-entry repos, silently deploying a target that was not configured for
// releases.
//
// Loop 50 times so map-order randomness would surface: with the bug, some
// iterations would enqueue a build (whichever entry the map iteration
// happened to yield first); with the fix, every iteration ignores.
func TestHandler_ReleaseTarget_NoReleaseTarget_Ignored(t *testing.T) {
	t.Parallel()

	// Two push-based entries for the same repo, neither deploy_on: release.
	// A release event must ignore both — never fall back to first-match.
	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/multi": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"multi-prod"},
			},
			"anatolykoptev/multi#dev": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"multi-dev"},
				Branch:      "dev",
			},
		},
	}

	for i := 0; i < 50; i++ {
		q, _ := newTestQueue()
		h := NewHandler(cfg, q, func(string) {})
		h.shaResolver = noResolveSHA

		body := releasePayload("anatolykoptev/multi", "v1.0.0", "main")
		w := postRelease(h, body)

		if w.Code != http.StatusOK {
			t.Fatalf("iter %d: status = %d", i, w.Code)
		}
		var resp map[string]string
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp["status"] != "ignored" {
			t.Fatalf("iter %d: status = %q, want ignored (no deploy_on: release target — "+
				"first-match fallback would have enqueued a random target, the #169 bug)", i, resp["status"])
		}
		if resp["reason"] != "no release-triggered target configured" {
			t.Fatalf("iter %d: reason = %q, want %q", i, resp["reason"], "no release-triggered target configured")
		}
		if q.queuedHas(serviceKey([]string{"multi-prod"})) || q.queuedHas(serviceKey([]string{"multi-dev"})) {
			t.Fatalf("iter %d: no build must be enqueued for a repo with no release target", i)
		}
		h.Close()
	}
}

// TestHandler_ReleaseTarget_MultipleReleaseTargets_DeploysAll verifies that
// a repo with MORE THAN ONE deploy_on: release entry deploys ALL of them on
// a release event. This is a genuine multi-target release (e.g. two services
// that both ship on release). The decision to deploy all (rather than treat
// as a config error) is deliberate: it mirrors the push path's multi-target
// fan-out, and each target is independently gated by its own BuildPaths
// filter. The event is logged explicitly so the operator sees the fan-out.
func TestHandler_ReleaseTarget_MultipleReleaseTargets_DeploysAll(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/dual": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"dual-a"},
				DeployOn:    "release",
			},
			"anatolykoptev/dual#b": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"dual-b"},
				DeployOn:    "release",
			},
		},
	}

	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = noResolveSHA

	body := releasePayload("anatolykoptev/dual", "v1.0.0", "main")
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	// Multi-target path: aggregated status (both queued → "queued,queued").
	if resp["status"] != "queued,queued" {
		t.Errorf("status = %q, want %q (both release targets deployed)", resp["status"], "queued,queued")
	}
	if !q.queuedHas(serviceKey([]string{"dual-a"})) {
		t.Error("dual-a (deploy_on: release) must be enqueued")
	}
	if !q.queuedHas(serviceKey([]string{"dual-b"})) {
		t.Error("dual-b (deploy_on: release) must be enqueued")
	}
}

// TestHandler_ReleaseTarget_SingleEntry_DeploysUnchanged is the regression
// guard: a single-entry repo with deploy_on: release deploys on a release
// event exactly as before — the single-target response contract (status /
// repo / commit fields) is preserved.
func TestHandler_ReleaseTarget_SingleEntry_DeploysUnchanged(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/solo": {
				ComposePath: "/tmp",
				SourcePath:  "/tmp",
				Services:    []string{"solo"},
				DeployOn:    "release",
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = noResolveSHA

	body := releasePayload("anatolykoptev/solo", "v1.0.0", "main")
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want queued (single-entry release target — unchanged)", resp["status"])
	}
	if resp["repo"] != "anatolykoptev/solo" {
		t.Errorf("repo = %q, want %q", resp["repo"], "anatolykoptev/solo")
	}
	if !q.queuedHas(serviceKey([]string{"solo"})) {
		t.Error("solo must be enqueued")
	}
}

// TestHandler_ReleaseTarget_MultiTarget_DiffDoesNotLeakBetweenTargets is the
// regression for the per-target diff isolation bug in the multi-target release
// loop. attachReleaseDiff writes the resolved changed-files list into the
// SHARED push value's Commits field. When target A resolves a list and target
// B's own diff cannot be resolved (attachReleaseDiff is a no-op for B, leaving
// Commits untouched), B used to inherit A's Commits and be path-filtered
// against A's files — silently skipping a build that should have been
// conservative ("no build-relevant files changed" reported when the truth is
// "we could not determine what changed").
//
// Setup: two deploy_on: release targets, key-sorted so A ("owner/leak") is
// processed before B ("owner/leak#b").
//   - A's SourcePath is a real git fixture; its diff resolves to [CHANGELOG.md]
//     (outside A's BuildPaths app/**) → A is correctly skipped.
//   - B's SourcePath is a different dir whose deployed SHA resolves to
//     "unknown" (shaResolver returns "unknown" for B's dir) → B's diff cannot
//     be resolved → attachReleaseDiff is a no-op for B.
//
// With the bug: B inherits A's [CHANGELOG.md], which is outside B's BuildPaths
// app/** → B is skipped → status "skipped,skipped", B not enqueued.
// With the fix: B gets its own push copy with empty Commits → skipByPathFilter's
// "elided diff — build conservatively" fallback fires → B is queued → status
// "skipped,queued", B enqueued. B being queued (not skipped) is itself the
// proof that B's gating did not see A's file list: had it seen [CHANGELOG.md],
// it would have been skipped.
func TestHandler_ReleaseTarget_MultiTarget_DiffDoesNotLeakBetweenTargets(t *testing.T) {
	t.Parallel()

	// A's source clone: two commits. First creates app/main.go (the
	// "currently deployed" state), second touches ONLY CHANGELOG.md (the
	// release-please commit at target_commitish).
	sourceA, deployedSHA, targetSHA := buildTwoCommitFixture(t, "CHANGELOG.md", "# Changelog\n")

	// B's source clone: a real git repo, but the shaResolver below returns
	// "unknown" for this dir so releaseChangedFiles short-circuits before
	// any git diff — mirroring a fresh/never-deployed target whose diff
	// cannot be resolved.
	sourceB := t.TempDir()
	mustRun(t, sourceB, "git", "init", "--initial-branch=main")
	mustRun(t, sourceB, "git", "config", "user.email", "test@test.com")
	mustRun(t, sourceB, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(sourceB, "README.md"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, sourceB, "git", "add", ".")
	mustRun(t, sourceB, "git", "commit", "-m", "init")

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/leak": {
				ComposePath: sourceA,
				SourcePath:  sourceA,
				Services:    []string{"leak-a"},
				BuildPaths:  []string{"app/**"},
				DeployOn:    "release",
			},
			"anatolykoptev/leak#b": {
				ComposePath: sourceB,
				SourcePath:  sourceB,
				Services:    []string{"leak-b"},
				BuildPaths:  []string{"app/**"},
				DeployOn:    "release",
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	// Resolve a real deployed SHA for A's clone; "unknown" for B's clone so
	// B's diff cannot be resolved (attachReleaseDiff no-op for B).
	h.shaResolver = func(_ context.Context, dir string) string {
		if dir == sourceA {
			return deployedSHA
		}
		return "unknown"
	}

	body := releasePayload("anatolykoptev/leak", "v1.0.0", targetSHA)
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	// A: diff [CHANGELOG.md] outside app/** → skipped.
	// B: diff unresolvable → conservative build → queued (NOT skipped on
	// A's leaked [CHANGELOG.md]).
	if resp["status"] != "skipped,queued" {
		t.Fatalf("status = %q, want %q "+
			"(A correctly skipped on its own diff; B must build conservatively "+
			"because its diff is unresolvable — a skip here means B inherited "+
			"A's changed-files list, the per-target diff leak bug)",
			resp["status"], "skipped,queued")
	}
	if q.queuedHas(serviceKey([]string{"leak-a"})) {
		t.Error("leak-a must NOT be enqueued (CHANGELOG.md is outside its build_paths app/**)")
	}
	if !q.queuedHas(serviceKey([]string{"leak-b"})) {
		t.Error("leak-b must be enqueued — its diff is unresolvable so it must " +
			"build conservatively, not be skipped against leak-a's file list")
	}
}
